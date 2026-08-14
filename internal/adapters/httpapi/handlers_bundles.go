package httpapi

import (
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/uniandes-isis4426/okfp/internal/adapters/httpapi/mw"
	"github.com/uniandes-isis4426/okfp/internal/adapters/httpapi/problem"
	"github.com/uniandes-isis4426/okfp/internal/app/apiapp"
	"github.com/uniandes-isis4426/okfp/internal/bundlezip"
	"github.com/uniandes-isis4426/okfp/internal/domain"
	"github.com/uniandes-isis4426/okfp/internal/platform/logging"
)

type BundleHandler struct{ svc *apiapp.BundleService }

func (h *BundleHandler) List(w http.ResponseWriter, r *http.Request) {
	p := mw.MustPrincipal(r.Context())
	limit, offset := pagination(r)
	items, err := h.svc.List(r.Context(), p.UserID, limit, offset)
	if err != nil {
		problem.Write(w, r, err)
		return
	}
	problem.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *BundleHandler) Get(w http.ResponseWriter, r *http.Request) {
	p := mw.MustPrincipal(r.Context())
	id, ok := parseID(w, r, "bundleID")
	if !ok {
		return
	}
	b, err := h.svc.Get(r.Context(), p.UserID, id)
	if err != nil {
		problem.Write(w, r, err)
		return
	}
	problem.JSON(w, http.StatusOK, b)
}

// GetFile sirve un fichero suelto del bundle para el visor.
func (h *BundleHandler) GetFile(w http.ResponseWriter, r *http.Request) {
	p := mw.MustPrincipal(r.Context())
	id, ok := parseID(w, r, "bundleID")
	if !ok {
		return
	}
	rel := chi.URLParam(r, "*")

	rc, ctype, err := h.svc.OpenFile(r.Context(), p.UserID, id, rel)
	if err != nil {
		problem.Write(w, r, err)
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Content-Disposition inline solo tiene sentido para texto ya neutralizado;
	// se fuerza descarga para todo lo demas.
	w.Header().Set("Cache-Control", "private, max-age=60")
	if _, err := io.Copy(w, rc); err != nil {
		logging.From(r.Context()).Warn("error copiando fichero del bundle", "err", err.Error())
	}
}

// CreateTicket emite el ticket de descarga de un solo uso.
func (h *BundleHandler) CreateTicket(w http.ResponseWriter, r *http.Request) {
	p := mw.MustPrincipal(r.Context())
	id, ok := parseID(w, r, "bundleID")
	if !ok {
		return
	}
	token, exp, err := h.svc.CreateTicket(r.Context(), p.UserID, id)
	if err != nil {
		problem.Write(w, r, err)
		return
	}
	problem.JSON(w, http.StatusCreated, map[string]any{
		"ticket":     token,
		"expires_at": exp,
		"url":        "/api/v1/bundles/" + id.String() + "/download?t=" + token,
	})
}

// Download sirve el bundle completo como ZIP generado al vuelo.
//
// Puntos criticos de este handler:
//
//   - TODA la autorizacion y la comprobacion de existencia ocurren ANTES de
//     escribir el primer byte. En cuanto se escribe, ya se ha enviado 200 OK y
//     no hay forma legitima de responder 403 o 404.
//   - El paquete NUNCA se materializa: el consumo de memoria es el de un buffer
//     de copia, sea el bundle de 3 KB o de 300 MB.
//   - No se fija Content-Length porque el tamano final no se conoce hasta
//     escribir el directorio central del ZIP.
//   - Si falla a mitad, se aborta con http.ErrAbortHandler: el cliente recibe un
//     ZIP sin directorio central, que cualquier herramienta rechaza. Una
//     corrupcion detectable es preferible a una silenciosa.
func (h *BundleHandler) Download(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("t")
	if token == "" {
		problem.Write(w, r, domain.ErrTicketInvalid.WithMessage(
			"Falta el ticket de descarga"))
		return
	}

	plan, err := h.svc.DownloadByTicket(r.Context(), token)
	if err != nil {
		problem.Write(w, r, err)
		return
	}

	// Plazo de escritura renovable: sin esto, un cliente lento podria mantener
	// la conexion indefinidamente. No se usa un timeout global de peticion
	// porque cortaria descargas legitimas grandes.
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Minute))
	}

	bundlezip.WriteHeaders(w, plan.Name)

	src := h.svc.Source(plan.Bundle.Prefix)
	if err := bundlezip.StreamZip(r.Context(), w, src, plan.Entries); err != nil {
		logging.From(r.Context()).Error("fallo a mitad de la descarga",
			"bundle_id", plan.Bundle.ID.String(), "err", err.Error())
		// La respuesta ya empezo: abortar es la unica senal honesta.
		panic(http.ErrAbortHandler)
	}
}
