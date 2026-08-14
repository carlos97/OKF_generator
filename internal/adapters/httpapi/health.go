package httpapi

import (
	"net/http"

	"github.com/uniandes-isis4426/okfp/internal/adapters/httpapi/problem"
)

// HealthHandler expone las sondas.
//
// Se distinguen dos: /healthz responde en cuanto el proceso vive (lo usa el
// healthcheck del contenedor para no reiniciarlo en bucle mientras arranca
// PostgreSQL) y /readyz comprueba de verdad las dependencias.
type HealthHandler struct {
	Checks  map[string]func() error
	Version string
}

func (h *HealthHandler) Live(w http.ResponseWriter, r *http.Request) {
	problem.JSON(w, http.StatusOK, map[string]any{
		"status": "ok", "version": h.Version,
	})
}

func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	results := map[string]string{}
	ok := true
	for name, check := range h.Checks {
		if err := check(); err != nil {
			results[name] = "error: " + err.Error()
			ok = false
			continue
		}
		results[name] = "ok"
	}
	status := http.StatusOK
	if !ok {
		status = http.StatusServiceUnavailable
	}
	problem.JSON(w, status, map[string]any{"ready": ok, "checks": results})
}
