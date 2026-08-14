package apiapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/uniandes-isis4426/okfp/internal/adapters/objectstore"
	"github.com/uniandes-isis4426/okfp/internal/adapters/postgres"
	"github.com/uniandes-isis4426/okfp/internal/config"
	"github.com/uniandes-isis4426/okfp/internal/domain"
	"github.com/uniandes-isis4426/okfp/internal/platform/logging"
)

// Enqueuer publica el trabajo en la cola.
type Enqueuer interface {
	Publish(ctx context.Context, msg domain.JobMessage) error
}

// DocumentService atiende la carga de documentos.
type DocumentService struct {
	docs   *postgres.DocumentRepo
	jobs   *postgres.JobRepo
	store  *objectstore.Store
	queue  Enqueuer
	limits config.LimitConfig
	pdf    bool
}

func NewDocumentService(
	docs *postgres.DocumentRepo, jobs *postgres.JobRepo,
	store *objectstore.Store, q Enqueuer, limits config.LimitConfig, pdfEnabled bool,
) *DocumentService {
	return &DocumentService{docs: docs, jobs: jobs, store: store, queue: q, limits: limits, pdf: pdfEnabled}
}

// UploadResult es la respuesta inmediata de la carga.
type UploadResult struct {
	JobID      uuid.UUID `json:"job_id"`
	DocumentID uuid.UUID `json:"document_id"`
	Status     string    `json:"status"`
	Filename   string    `json:"filename"`
	Format     string    `json:"format"`
	SizeBytes  int64     `json:"size_bytes"`
}

// Upload recibe el documento y devuelve de inmediato el identificador del
// trabajo, SIN convertir nada.
//
// Camino de los bytes: el cuerpo multipart se copia en STREAMING directamente al
// almacenamiento de objetos. No se usa ParseMultipartForm porque escribe a disco
// del contenedor a partir de cierto tamano, lo que romperia la regla de API sin
// estado; y el limite se aplica con http.MaxBytesReader (que devuelve error al
// superarse) y no con io.LimitReader, que truncaria el documento en silencio y
// guardaria un fichero mutilado como si fuera valido.
//
// ORDEN DE OPERACIONES, y es lo importante de esta funcion:
//
//  1. Subir el objeto. Si algo falla despues, sobra un objeto; nunca falta.
//  2. COMMIT de documento + trabajo en una transaccion.
//  3. Publicar en la cola y esperar el confirm.
//
// Publicar antes del commit abre una carrera real: con el worker ocioso el
// consumo ocurre en milisegundos, el worker recibiria un job_id que aun no
// existe y el trabajo se perderia de forma intermitente, que es el peor modo de
// fallo posible para una sustentacion.
func (s *DocumentService) Upload(
	ctx context.Context, ownerID uuid.UUID, part *multipart.Part, declaredType string,
) (*UploadResult, error) {
	filename := filepath.Base(part.FileName())
	if filename == "" || filename == "." || filename == string(filepath.Separator) {
		return nil, domain.ErrValidation.WithMessage("El fichero no tiene nombre")
	}
	if !domain.IsAllowedExtension(filename, s.pdf) {
		return nil, domain.ErrUnsupported.WithMessage(fmt.Sprintf(
			"Extension no permitida. Formatos aceptados: %s",
			strings.Join(domain.AllowedExtensions(s.pdf), ", ")))
	}

	// Sniff de los primeros 512 bytes y reinyeccion, para no consumir el flujo.
	head := make([]byte, 512)
	n, err := io.ReadFull(part, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, domain.ErrValidation.Wrap(err)
	}
	head = head[:n]
	if n == 0 {
		return nil, domain.ErrEmptyFile
	}

	det := domain.DetectFormat(head, filename)
	if det.Format == domain.FormatUnknown {
		return nil, domain.ErrUnsupported.WithMessage(
			"El contenido del fichero no corresponde a ningun formato soportado")
	}
	if det.Format == domain.FormatPDF && !s.pdf {
		return nil, domain.ErrUnsupported.WithMessage(
			"El soporte de PDF esta desactivado en esta instalacion")
	}

	documentID := uuid.New()
	key := objectstore.OriginalKey(ownerID.String(), documentID.String())

	// Se cuenta el tamano y se calcula el hash al vuelo, mientras los bytes
	// viajan al almacenamiento: una sola pasada, sin bufferizar el documento.
	counter := &countingHasher{h: sha256.New()}
	body := io.TeeReader(io.MultiReader(strings.NewReader(string(head)), part), counter)

	if _, err := s.store.PutStream(ctx, s.store.BucketOriginals(), key, body, det.MediaType); err != nil {
		// Puede venir de MaxBytesReader si el cliente excedio el limite.
		if isTooLarge(err) {
			return nil, domain.ErrFileTooLarge
		}
		return nil, err
	}
	if counter.n == 0 {
		return nil, domain.ErrEmptyFile
	}
	if s.limits.MaxUploadBytes > 0 && counter.n > s.limits.MaxUploadBytes {
		// Defensa en profundidad si el limite del transporte no salto.
		_ = s.store.RemovePrefix(ctx, s.store.BucketOriginals(), key)
		return nil, domain.ErrFileTooLarge
	}

	doc := &domain.Document{
		ID: documentID, OwnerID: ownerID, Filename: filename,
		Format: det.Format, MediaType: det.MediaType,
		SizeBytes: counter.n, SHA256: counter.sum(), StorageKey: key,
	}
	job := &domain.Job{
		ID: uuid.New(), OwnerID: ownerID, DocumentID: documentID,
		Status: domain.JobQueued, MaxAttempts: 3,
	}

	if err := s.jobs.CreateWithDocument(ctx, doc, job); err != nil {
		return nil, err
	}

	msg := domain.JobMessage{
		JobID: job.ID, OwnerID: ownerID, DocumentID: documentID, Attempt: 1,
	}
	if err := s.queue.Publish(ctx, msg); err != nil {
		// El trabajo ya esta persistido: el barredor lo republicara. Se informa
		// del problema pero NO se pierde el trabajo.
		logging.From(ctx).Error("no se pudo publicar el trabajo en la cola",
			"job_id", job.ID.String(), "err", err.Error())
		return nil, domain.ErrUnavailable.WithMessage(
			"El documento se guardo pero la cola no confirmo el encolado; el trabajo se reintentara automaticamente").Wrap(err)
	}
	if err := s.jobs.MarkEnqueued(ctx, job.ID); err != nil {
		logging.From(ctx).Warn("no se pudo marcar el encolado confirmado",
			"job_id", job.ID.String(), "err", err.Error())
	}

	return &UploadResult{
		JobID: job.ID, DocumentID: documentID, Status: string(domain.JobQueued),
		Filename: filename, Format: string(det.Format), SizeBytes: counter.n,
	}, nil
}

func (s *DocumentService) List(ctx context.Context, ownerID uuid.UUID, limit, offset int) ([]domain.Document, error) {
	return s.docs.List(ctx, ownerID, limit, offset)
}

func (s *DocumentService) Get(ctx context.Context, ownerID, id uuid.UUID) (*domain.Document, error) {
	return s.docs.Get(ctx, ownerID, id)
}

func (s *DocumentService) Delete(ctx context.Context, ownerID, id uuid.UUID) error {
	return s.docs.SoftDelete(ctx, ownerID, id)
}

// OpenOriginal abre el documento tal y como se subio.
//
// Es util para comparar la entrada con el bundle generado, que es justo lo que se
// quiere hacer al revisar una conversion. Se sirve por STREAMING desde el
// almacenamiento, sin materializarlo en memoria de la API, igual que el ZIP.
//
// La propiedad se resuelve con el repositorio, que lleva ownerID en la firma: un
// documento ajeno o inexistente produce el mismo 404, sin revelar cual de los dos
// casos es.
func (s *DocumentService) OpenOriginal(
	ctx context.Context, ownerID, id uuid.UUID,
) (io.ReadCloser, *domain.Document, error) {
	doc, err := s.docs.Get(ctx, ownerID, id)
	if err != nil {
		return nil, nil, err
	}
	rc, err := s.store.Get(ctx, s.store.BucketOriginals(), doc.StorageKey)
	if err != nil {
		return nil, nil, err
	}
	return rc, doc, nil
}

// --- helpers ----------------------------------------------------------------

type countingHasher struct {
	h hash.Hash
	n int64
}

func (c *countingHasher) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return c.h.Write(p)
}

func (c *countingHasher) sum() string { return hex.EncodeToString(c.h.Sum(nil)) }

func isTooLarge(err error) bool {
	if err == nil {
		return false
	}
	var mbe *http.MaxBytesError
	if ok := asMaxBytes(err, &mbe); ok {
		return true
	}
	return strings.Contains(err.Error(), "http: request body too large")
}
