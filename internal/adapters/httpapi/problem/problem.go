// Package problem serializa los errores de la API con una forma unica.
//
// Es un paquete HOJA a proposito: no importa httpapi ni mw. Si el escritor de
// errores viviera en httpapi y los middlewares lo usaran, se produciria un
// ciclo de importacion (httpapi -> mw -> httpapi) que ni siquiera compila.
package problem

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/uniandes-isis4426/okfp/internal/domain"
)

// Problem sigue RFC 7807 y lo extiende con un codigo de dominio estable y el
// identificador de peticion, para que el frontend pueda reaccionar por codigo
// (y no por texto) y el soporte pueda correlacionar con los logs.
type Problem struct {
	Type      string              `json:"type"`
	Title     string              `json:"title"`
	Status    int                 `json:"status"`
	Code      string              `json:"code"`
	Detail    string              `json:"detail,omitempty"`
	RequestID string              `json:"request_id,omitempty"`
	Errors    []domain.FieldError `json:"errors,omitempty"`
}

const contentType = "application/problem+json"

// Write traduce cualquier error a una respuesta consistente.
//
// Los errores internos NO exponen su causa al cliente: se registra el detalle
// en el log y se devuelve un mensaje genérico. Filtrar mensajes de la base de
// datos o del almacenamiento en la respuesta seria una fuga de informacion.
func Write(w http.ResponseWriter, r *http.Request, err error) {
	de := domain.AsError(err)
	requestID := RequestIDFrom(r)

	p := Problem{
		Type:      "about:blank",
		Title:     de.Message,
		Status:    de.Status,
		Code:      de.Code,
		RequestID: requestID,
		Errors:    de.Fields,
	}

	if de.Status >= 500 {
		slog.Default().Error("error interno de la API",
			"code", de.Code, "request_id", requestID, "err", err.Error(),
			"method", r.Method, "path", r.URL.Path)
		p.Title = domain.ErrInternal.Message
		p.Code = domain.ErrInternal.Code
	} else {
		slog.Default().Debug("error de cliente",
			"code", de.Code, "request_id", requestID, "status", de.Status,
			"method", r.Method, "path", r.URL.Path)
	}

	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// JSON escribe una respuesta correcta.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// --- request id -------------------------------------------------------------

type ctxKey struct{}

func WithRequestID(r *http.Request, id string) *http.Request {
	return r.WithContext(contextWithRequestID(r, id))
}

func RequestIDFrom(r *http.Request) string {
	if v, ok := r.Context().Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}
