package httpapi

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/uniandes-isis4426/okfp/internal/adapters/httpapi/mw"
	"github.com/uniandes-isis4426/okfp/internal/adapters/httpapi/problem"
	"github.com/uniandes-isis4426/okfp/internal/app/apiapp"
	"github.com/uniandes-isis4426/okfp/internal/config"
	"github.com/uniandes-isis4426/okfp/internal/domain"
	"github.com/uniandes-isis4426/okfp/internal/platform/logging"
)

type DocumentHandler struct {
	svc    *apiapp.DocumentService
	limits config.LimitConfig
	pdf    bool
}

// Upload recibe el documento y responde de inmediato con el identificador del
// trabajo.
//
// Se usa r.MultipartReader() y NO r.ParseMultipartForm(): este ultimo vuelca a
// disco del contenedor a partir de un umbral, lo que convertiria a la API en un
// servicio con estado en disco. Con el lector por partes los bytes van del
// socket al almacenamiento de objetos sin tocar el sistema de ficheros local.
//
// El limite de tamano se aplica con http.MaxBytesReader, que ABORTA al superarse
// (413). io.LimitReader truncaria el documento en silencio y se guardaria un
// fichero mutilado que despues se convertiria como si estuviera completo.
func (h *DocumentHandler) Upload(w http.ResponseWriter, r *http.Request) {
	if h.limits.MaxUploadBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, h.limits.MaxUploadBytes+1<<16)
	}

	ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(ct, "multipart/") {
		problem.Write(w, r, domain.ErrValidation.WithMessage(
			"Se esperaba una peticion multipart/form-data con el campo 'file'"))
		return
	}

	mr, err := r.MultipartReader()
	if err != nil {
		problem.Write(w, r, domain.ErrValidation.Wrap(err))
		return
	}

	p := mw.MustPrincipal(r.Context())

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			problem.Write(w, r, domain.ErrValidation.Wrap(err))
			return
		}
		if part.FormName() != "file" {
			_ = part.Close()
			continue
		}

		res, err := h.svc.Upload(r.Context(), p.UserID, part, part.Header.Get("Content-Type"))
		_ = part.Close()
		if err != nil {
			problem.Write(w, r, err)
			return
		}

		// 202 Accepted: el trabajo se acepto pero NO se ha procesado. Es el
		// codigo semanticamente correcto para un flujo asincrono y deja claro en
		// el propio contrato que la conversion no ocurre en esta peticion.
		problem.JSON(w, http.StatusAccepted, res)
		return
	}

	problem.Write(w, r, domain.ErrValidation.WithMessage(
		"La peticion no incluye el campo 'file'"))
}

func (h *DocumentHandler) List(w http.ResponseWriter, r *http.Request) {
	p := mw.MustPrincipal(r.Context())
	limit, offset := pagination(r)
	docs, err := h.svc.List(r.Context(), p.UserID, limit, offset)
	if err != nil {
		problem.Write(w, r, err)
		return
	}
	problem.JSON(w, http.StatusOK, map[string]any{"items": docs})
}

func (h *DocumentHandler) Get(w http.ResponseWriter, r *http.Request) {
	p := mw.MustPrincipal(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "documentID"))
	if err != nil {
		// Un identificador mal formado responde igual que uno inexistente: no
		// se distingue "no valido" de "no es tuyo".
		problem.Write(w, r, domain.ErrNotFound)
		return
	}
	doc, err := h.svc.Get(r.Context(), p.UserID, id)
	if err != nil {
		problem.Write(w, r, err)
		return
	}
	problem.JSON(w, http.StatusOK, doc)
}

// Content devuelve el documento original tal y como se subio.
//
// Se copia por streaming desde el almacenamiento al ResponseWriter: el fichero
// no pasa por la memoria de la API. Como el tamano SI se conoce (esta en los
// metadatos), aqui si se puede anunciar Content-Length y el navegador muestra
// una barra de progreso con porcentaje, a diferencia del ZIP del bundle.
func (h *DocumentHandler) Content(w http.ResponseWriter, r *http.Request) {
	p := mw.MustPrincipal(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "documentID"))
	if err != nil {
		problem.Write(w, r, domain.ErrNotFound)
		return
	}

	rc, doc, err := h.svc.OpenOriginal(r.Context(), p.UserID, id)
	if err != nil {
		problem.Write(w, r, err)
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", doc.MediaType)
	w.Header().Set("Content-Length", strconv.FormatInt(doc.SizeBytes, 10))
	// Siempre como adjunto: el original es contenido NO confiable y no debe
	// renderizarse en el origen de la aplicacion.
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", sanitizeFilename(doc.Filename)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=60")

	if _, err := io.Copy(w, rc); err != nil {
		logging.From(r.Context()).Warn("error enviando el documento original",
			"document_id", id.String(), "err", err.Error())
	}
}

// sanitizeFilename evita que un nombre de fichero del usuario rompa la cabecera
// Content-Disposition o inyecte cabeceras nuevas.
func sanitizeFilename(name string) string {
	name = strings.Map(func(r rune) rune {
		switch r {
		case '"', '\\', '\r', '\n':
			return '_'
		}
		if r < 0x20 {
			return '_'
		}
		return r
	}, name)
	if name == "" {
		return "documento"
	}
	return name
}

func (h *DocumentHandler) Delete(w http.ResponseWriter, r *http.Request) {
	p := mw.MustPrincipal(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "documentID"))
	if err != nil {
		problem.Write(w, r, domain.ErrNotFound)
		return
	}
	if err := h.svc.Delete(r.Context(), p.UserID, id); err != nil {
		problem.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Limits publica los limites de ingesta para que el frontend valide en cliente
// con los mismos numeros que aplica el servidor.
func (h *DocumentHandler) Limits(w http.ResponseWriter, r *http.Request) {
	problem.JSON(w, http.StatusOK, map[string]any{
		"max_upload_bytes":   h.limits.MaxUploadBytes,
		"allowed_extensions": domain.AllowedExtensions(h.pdf),
		"max_units":          h.limits.MaxUnits,
	})
}

func pagination(r *http.Request) (limit, offset int) {
	limit, offset = 50, 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
}
