package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/uniandes-isis4426/okfp/internal/adapters/httpapi/mw"
	"github.com/uniandes-isis4426/okfp/internal/adapters/httpapi/problem"
	"github.com/uniandes-isis4426/okfp/internal/app/apiapp"
	"github.com/uniandes-isis4426/okfp/internal/domain"
)

type AuthHandler struct{ svc *apiapp.AuthService }

func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var c apiapp.Credentials
	if err := decodeJSON(r, &c); err != nil {
		problem.Write(w, r, err)
		return
	}
	s, err := h.svc.Register(r.Context(), c)
	if err != nil {
		problem.Write(w, r, err)
		return
	}
	problem.JSON(w, http.StatusCreated, s)
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var c apiapp.Credentials
	if err := decodeJSON(r, &c); err != nil {
		problem.Write(w, r, err)
		return
	}
	s, err := h.svc.Login(r.Context(), c)
	if err != nil {
		problem.Write(w, r, err)
		return
	}
	problem.JSON(w, http.StatusOK, s)
}

func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	p := mw.MustPrincipal(r.Context())
	problem.JSON(w, http.StatusOK, map[string]any{
		"id":    p.UserID,
		"email": p.Email,
	})
}

// decodeJSON limita el cuerpo y rechaza campos desconocidos, para que un error
// de nombre en el cliente se detecte de inmediato en lugar de ignorarse.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return domain.ErrValidation.WithMessage("El cuerpo de la peticion no es JSON valido").Wrap(err)
	}
	return nil
}
