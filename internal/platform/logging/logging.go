// Package logging configura el logger estructurado del proyecto.
//
// Todo log es JSON con slog. La observabilidad del flujo de trabajos se apoya
// en dos cosas: estos logs correlacionados por request_id/job_id y la tabla
// job_events, que es una traza auditable y consultable desde el frontend. No se
// despliega Prometheus/Grafana: anadiria dos servicios al `docker compose up`
// sin mejorar la evidencia que se puede mostrar.
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

type ctxKey struct{}

// New crea el logger raiz.
func New(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}

	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})
	return slog.New(h)
}

// Into guarda un logger (normalmente ya enriquecido con request_id o job_id) en
// el contexto, para que las capas inferiores no tengan que recibirlo por
// parametro en cada firma.
func Into(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// From recupera el logger del contexto; si no hay ninguno, devuelve el default.
func From(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}
