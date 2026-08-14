// Package httpapi expone la API HTTP.
package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"log/slog"

	"github.com/uniandes-isis4426/okfp/internal/adapters/httpapi/mw"
	"github.com/uniandes-isis4426/okfp/internal/app/apiapp"
	"github.com/uniandes-isis4426/okfp/internal/config"
)

// Deps son las dependencias del router.
type Deps struct {
	Auth      *apiapp.AuthService
	Documents *apiapp.DocumentService
	Jobs      *apiapp.JobService
	Bundles   *apiapp.BundleService
	Health    *HealthHandler
	Cfg       *config.Config
	Logger    *slog.Logger
}

// NewRouter construye el arbol de rutas.
//
// Detalle importante: middleware.Timeout NO se registra en el router raiz.
// En chi, un middleware de la raiz se aplica a TODAS las rutas descendientes y
// no puede desaplicarse en un subrouter; con un timeout global de 30 s, la
// subida de un documento grande, la lectura de un fichero del bundle y sobre
// todo la descarga del ZIP se cortarian a mitad, entregando un archivo truncado
// que no abre. Por eso las rutas se agrupan en dos: las normales, con timeout, y
// las de streaming, sin el, donde el control temporal se hace por escritura con
// http.NewResponseController.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()

	r.Use(mw.RequestID)
	r.Use(mw.Recover)
	r.Use(mw.Logger(d.Logger))
	r.Use(mw.SecurityHeaders)
	r.Use(chimw.RealIP)

	// Sondas de salud: sin autenticacion, las usa el healthcheck de Compose.
	r.Get("/healthz", d.Health.Live)
	r.Get("/readyz", d.Health.Ready)

	ah := &AuthHandler{svc: d.Auth}
	dh := &DocumentHandler{svc: d.Documents, limits: d.Cfg.Limit, pdf: d.Cfg.Work.EnablePDF}
	jh := &JobHandler{svc: d.Jobs, devTools: d.Cfg.DevTools}
	bh := &BundleHandler{svc: d.Bundles}

	r.Route("/api/v1", func(r chi.Router) {
		// --- publico ---
		r.Group(func(r chi.Router) {
			r.Use(chimw.Timeout(30 * time.Second))
			r.Post("/auth/register", ah.Register)
			r.Post("/auth/login", ah.Login)
			r.Get("/meta/limits", dh.Limits)
		})

		// --- autenticado, peticiones normales ---
		r.Group(func(r chi.Router) {
			r.Use(mw.RequireAuth(d.Auth))
			r.Use(chimw.Timeout(30 * time.Second))

			r.Get("/me", ah.Me)

			r.Get("/documents", dh.List)
			r.Get("/documents/{documentID}", dh.Get)
			r.Delete("/documents/{documentID}", dh.Delete)

			r.Get("/jobs", jh.List)
			r.Get("/jobs/{jobID}", jh.Get)
			r.Post("/jobs/{jobID}/retry", jh.Retry)
			r.Post("/jobs/{jobID}/cancel", jh.Cancel)
			r.Post("/jobs/{jobID}/replay", jh.Replay)

			r.Get("/bundles", bh.List)
			r.Get("/bundles/{bundleID}", bh.Get)
			r.Post("/bundles/{bundleID}/download-tickets", bh.CreateTicket)
		})

		// --- autenticado, rutas de streaming: SIN timeout global ---
		r.Group(func(r chi.Router) {
			r.Use(mw.RequireAuth(d.Auth))
			r.Post("/documents", dh.Upload)
			r.Get("/documents/{documentID}/content", dh.Content)
			r.Get("/bundles/{bundleID}/files/*", bh.GetFile)
		})

		// --- descarga por ticket: la autorizacion la lleva el propio ticket ---
		r.Get("/bundles/{bundleID}/download", bh.Download)
	})

	return r
}
