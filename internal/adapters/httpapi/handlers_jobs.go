package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/uniandes-isis4426/okfp/internal/adapters/httpapi/mw"
	"github.com/uniandes-isis4426/okfp/internal/adapters/httpapi/problem"
	"github.com/uniandes-isis4426/okfp/internal/app/apiapp"
	"github.com/uniandes-isis4426/okfp/internal/domain"
)

type JobHandler struct {
	svc      *apiapp.JobService
	devTools bool
}

func (h *JobHandler) Get(w http.ResponseWriter, r *http.Request) {
	p := mw.MustPrincipal(r.Context())
	id, ok := parseID(w, r, "jobID")
	if !ok {
		return
	}
	view, err := h.svc.Get(r.Context(), p.UserID, id)
	if err != nil {
		problem.Write(w, r, err)
		return
	}
	problem.JSON(w, http.StatusOK, view)
}

func (h *JobHandler) List(w http.ResponseWriter, r *http.Request) {
	p := mw.MustPrincipal(r.Context())
	limit, offset := pagination(r)
	jobs, err := h.svc.List(r.Context(), p.UserID, limit, offset)
	if err != nil {
		problem.Write(w, r, err)
		return
	}
	problem.JSON(w, http.StatusOK, map[string]any{"items": jobs})
}

// Retry devuelve 200 con el hijo existente si ya se habia creado (doble clic) y
// 201 cuando lo crea. El cliente no recibe un error por pulsar dos veces.
func (h *JobHandler) Retry(w http.ResponseWriter, r *http.Request) {
	p := mw.MustPrincipal(r.Context())
	id, ok := parseID(w, r, "jobID")
	if !ok {
		return
	}
	child, err := h.svc.Retry(r.Context(), p.UserID, id)
	if err != nil {
		problem.Write(w, r, err)
		return
	}
	problem.JSON(w, http.StatusCreated, child)
}

func (h *JobHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	p := mw.MustPrincipal(r.Context())
	id, ok := parseID(w, r, "jobID")
	if !ok {
		return
	}
	status, err := h.svc.Cancel(r.Context(), p.UserID, id)
	if err != nil {
		problem.Write(w, r, err)
		return
	}
	// 200 si ya termino (estaba en cola), 202 si la cancelacion es cooperativa
	// y la atendera el worker en su siguiente punto de control.
	code := http.StatusOK
	if status == domain.JobCanceling {
		code = http.StatusAccepted
	}
	problem.JSON(w, code, map[string]any{"status": status})
}

// Replay reinyecta el mensaje en la cola para demostrar la idempotencia.
//
// Esta dentro del grupo autenticado y el servicio resuelve el trabajo POR
// PROPIETARIO, igual que cualquier otro endpoint. Una herramienta de
// demostracion que aceptara un identificador ajeno seria un fallo de
// aislamiento en la propia prueba de la ausencia de duplicados.
func (h *JobHandler) Replay(w http.ResponseWriter, r *http.Request) {
	if !h.devTools {
		problem.Write(w, r, domain.ErrNotFound)
		return
	}
	p := mw.MustPrincipal(r.Context())
	id, ok := parseID(w, r, "jobID")
	if !ok {
		return
	}
	times := 1
	if v := r.URL.Query().Get("times"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			times = n
		}
	}
	sent, err := h.svc.Replay(r.Context(), p.UserID, id, times)
	if err != nil {
		problem.Write(w, r, err)
		return
	}
	problem.JSON(w, http.StatusAccepted, map[string]any{"republished": sent})
}

func parseID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		problem.Write(w, r, domain.ErrNotFound)
		return uuid.Nil, false
	}
	return id, true
}
