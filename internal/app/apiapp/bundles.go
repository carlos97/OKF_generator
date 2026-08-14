package apiapp

import (
	"context"
	"io"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/uniandes-isis4426/okfp/internal/adapters/objectstore"
	"github.com/uniandes-isis4426/okfp/internal/adapters/postgres"
	"github.com/uniandes-isis4426/okfp/internal/bundlezip"
	"github.com/uniandes-isis4426/okfp/internal/config"
	"github.com/uniandes-isis4426/okfp/internal/domain"
)

// BundleService sirve metadatos, ficheros individuales y la descarga completa.
type BundleService struct {
	bundles *postgres.BundleRepo
	store   *objectstore.Store
	auth    *AuthService
	cfg     config.AuthConfig
}

func NewBundleService(b *postgres.BundleRepo, store *objectstore.Store, auth *AuthService, cfg config.AuthConfig) *BundleService {
	return &BundleService{bundles: b, store: store, auth: auth, cfg: cfg}
}

func (s *BundleService) List(ctx context.Context, ownerID uuid.UUID, limit, offset int) ([]domain.Bundle, error) {
	return s.bundles.List(ctx, ownerID, limit, offset)
}

// Get devuelve el manifiesto. Resuelve con GetPublished, que es la MISMA
// funcion que usan la lectura de ficheros y la descarga: compartirla impide que
// un endpoint nuevo olvide filtrar por propietario o por estado publicado y
// abra una ruta lateral a un bundle que no debia ser accesible.
func (s *BundleService) Get(ctx context.Context, ownerID, bundleID uuid.UUID) (*domain.Bundle, error) {
	return s.bundles.GetPublished(ctx, ownerID, bundleID)
}

// OpenFile sirve un fichero suelto del bundle, para el visor del frontend.
func (s *BundleService) OpenFile(ctx context.Context, ownerID, bundleID uuid.UUID, rel string) (io.ReadCloser, string, error) {
	b, err := s.bundles.GetPublished(ctx, ownerID, bundleID)
	if err != nil {
		return nil, "", err
	}

	clean := path.Clean("/" + strings.TrimPrefix(rel, "/"))[1:]
	if clean == "" || strings.Contains(clean, "..") {
		return nil, "", domain.ErrNotFound
	}

	// La ruta pedida debe estar en el MANIFIESTO. No basta con limpiarla:
	// comparar contra la lista de ficheros registrados elimina cualquier
	// posibilidad de leer un objeto que no pertenezca al bundle.
	var found bool
	for _, f := range b.Files {
		if f.Path == clean {
			found = true
			break
		}
	}
	if !found {
		return nil, "", domain.ErrNotFound
	}

	rc, err := s.store.Get(ctx, s.store.BucketBundles(), b.Prefix+clean)
	if err != nil {
		return nil, "", err
	}
	return rc, contentTypeFor(clean), nil
}

// CreateTicket emite el ticket de descarga. Aqui es donde se comprueba la
// propiedad y la publicacion; la descarga posterior solo canjea.
func (s *BundleService) CreateTicket(ctx context.Context, ownerID, bundleID uuid.UUID) (string, time.Time, error) {
	t, err := s.bundles.CreateTicket(ctx, ownerID, bundleID, s.cfg.TicketTTL, s.cfg.TicketMaxUses)
	if err != nil {
		return "", time.Time{}, err
	}
	token, err := s.auth.IssueTicket(t.ID, t.BundleID, t.OwnerID)
	if err != nil {
		return "", time.Time{}, err
	}
	return token, t.ExpiresAt, nil
}

// DownloadPlan es todo lo necesario para servir el ZIP, resuelto ANTES de
// escribir el primer byte de la respuesta.
type DownloadPlan struct {
	Bundle  *domain.Bundle
	Entries []bundlezip.Entry
	Name    string
}

// PrepareDownload valida acceso y existencia por completo antes de empezar a
// escribir.
//
// Es un requisito del streaming: una vez enviado el primer byte ya se ha
// respondido 200 OK y no hay forma legitima de cambiar el codigo de estado. Todo
// lo que pueda fallar con un 401, 403 o 404 tiene que fallar aqui.
func (s *BundleService) PrepareDownload(ctx context.Context, ownerID, bundleID uuid.UUID) (*DownloadPlan, error) {
	b, err := s.bundles.GetPublished(ctx, ownerID, bundleID)
	if err != nil {
		return nil, err
	}
	if len(b.Files) == 0 {
		return nil, domain.ErrBundleNotReady
	}

	entries := make([]bundlezip.Entry, 0, len(b.Files))
	for _, f := range b.Files {
		entries = append(entries, bundlezip.Entry{Path: f.Path, Size: f.SizeBytes})
	}
	return &DownloadPlan{
		Bundle:  b,
		Entries: entries,
		Name:    "bundle-" + b.ID.String() + ".zip",
	}, nil
}

// DownloadByTicket canjea un ticket firmado y devuelve el plan de descarga.
func (s *BundleService) DownloadByTicket(ctx context.Context, token string) (*DownloadPlan, error) {
	ticketID, bundleID, ownerID, err := s.auth.VerifyTicket(token)
	if err != nil {
		return nil, err
	}
	// El canje en base de datos es lo que hace el ticket realmente limitado:
	// la firma por si sola no puede contar usos.
	dbOwner, err := s.bundles.RedeemTicket(ctx, ticketID, bundleID)
	if err != nil {
		return nil, err
	}
	if dbOwner != ownerID {
		return nil, domain.ErrTicketInvalid
	}
	return s.PrepareDownload(ctx, ownerID, bundleID)
}

// Source adapta el almacenamiento a la interfaz del empaquetador.
func (s *BundleService) Source(prefix string) bundlezip.Source {
	return &storeSource{store: s.store, bucket: s.store.BucketBundles(), prefix: prefix}
}

type storeSource struct {
	store  *objectstore.Store
	bucket string
	prefix string
}

func (s *storeSource) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	return s.store.Get(ctx, s.bucket, s.prefix+p)
}

func contentTypeFor(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".md":
		// text/plain y no text/markdown: el navegador nunca debe intentar
		// renderizar contenido derivado del documento del usuario. El frontend
		// lo procesa con un renderizador que desactiva el HTML embebido.
		return "text/plain; charset=utf-8"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		// Un SVG servido como image/svg+xml puede ejecutar script en el
		// contexto del origen: se degrada a texto.
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}
