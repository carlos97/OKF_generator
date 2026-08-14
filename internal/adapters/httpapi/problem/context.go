package problem

import (
	"context"
	"net/http"
)

func contextWithRequestID(r *http.Request, id string) context.Context {
	return context.WithValue(r.Context(), ctxKey{}, id)
}

// RequestIDFromContext permite recuperar el identificador sin tener el
// *http.Request a mano (por ejemplo desde un servicio).
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}
