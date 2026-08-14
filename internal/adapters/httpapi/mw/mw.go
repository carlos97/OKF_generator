// Package mw contiene los middlewares HTTP.
//
// Importa problem y domain, NUNCA httpapi: esa direccion unica es lo que evita
// el ciclo de importacion.
package mw

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/uniandes-isis4426/okfp/internal/adapters/httpapi/problem"
	"github.com/uniandes-isis4426/okfp/internal/domain"
	"github.com/uniandes-isis4426/okfp/internal/platform/logging"
)

// RequestID asigna (o propaga) un identificador de peticion y lo deja en el
// contexto, en la respuesta y en el logger.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-Id", id)
		r = problem.WithRequestID(r, id)
		next.ServeHTTP(w, r)
	})
}

// Logger registra cada peticion en JSON.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Unwrap es imprescindible: sin el, http.NewResponseController no puede
// alcanzar el ResponseWriter real y las rutas de streaming perderian
// silenciosamente el control de plazos de escritura y el Flush.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func Logger(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}

			l := base.With("request_id", problem.RequestIDFrom(r))
			r = r.WithContext(logging.Into(r.Context(), l))

			next.ServeHTTP(sw, r)

			if sw.status == 0 {
				sw.status = http.StatusOK
			}
			l.Info("http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"bytes", sw.bytes,
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}

// Recover convierte un panic en un 500 con la forma de error habitual.
//
// http.ErrAbortHandler se re-propaga sin tocarlo: es la forma idiomatica de Go
// de abortar una respuesta ya iniciada, y la usa la descarga por streaming
// cuando falla a mitad. Tratarlo como un panic normal escribiria un cuerpo de
// error sobre un ZIP a medias.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				slog.Default().Error("panic en un handler",
					"request_id", problem.RequestIDFrom(r),
					"panic", rec,
					"stack", string(debug.Stack()))
				problem.Write(w, r, domain.ErrInternal)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// --- Autenticacion ----------------------------------------------------------

type principalKey struct{}

// Principal es el usuario autenticado de la peticion.
type Principal struct {
	UserID uuid.UUID
	Email  string
}

// Verifier valida un token y devuelve el principal.
type Verifier interface {
	Verify(token string) (*Principal, error)
}

// RequireAuth exige un Bearer valido.
func RequireAuth(v Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				problem.Write(w, r, domain.ErrUnauthorized)
				return
			}
			p, err := v.Verify(strings.TrimSpace(auth[7:]))
			if err != nil {
				problem.Write(w, r, domain.ErrUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), principalKey{}, p)
			ctx = logging.Into(ctx, logging.From(ctx).With("owner_id", p.UserID.String()))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// PrincipalFrom devuelve el usuario autenticado.
func PrincipalFrom(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(*Principal)
	return p, ok
}

// MustPrincipal se usa en handlers que ya estan detras de RequireAuth.
func MustPrincipal(ctx context.Context) *Principal {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		panic("MustPrincipal llamado en una ruta sin RequireAuth")
	}
	return p
}

// --- Cabeceras de seguridad -------------------------------------------------

func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
