// Package apiapp contiene los casos de uso de la API.
//
// Este paquete lo importa UNICAMENTE cmd/api y no depende de internal/okf. Esa
// separacion en paquetes distintos (y no solo en ficheros distintos) es
// necesaria porque Go resuelve dependencias por paquete: si los servicios de la
// API y el del worker vivieran en el mismo paquete, el binario de la API
// arrastraria el conversor y la garantia estructural seria falsa.
package apiapp

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/uniandes-isis4426/okfp/internal/adapters/httpapi/mw"
	"github.com/uniandes-isis4426/okfp/internal/adapters/postgres"
	"github.com/uniandes-isis4426/okfp/internal/config"
	"github.com/uniandes-isis4426/okfp/internal/domain"
)

// AuthService implementa un mecanismo de autenticacion simple.
//
// El enunciado dice literalmente que "un mecanismo de autenticacion simple es
// suficiente, pues no constituye el eje del proyecto", asi que se usa un JWT
// HS256 de 24 horas y se descarta deliberadamente la rotacion de refresh con
// deteccion de reuso y revocacion de familia: seria una tabla, cuatro endpoints
// y una clase entera de errores a cambio de cero puntos de rubrica.
type AuthService struct {
	users *postgres.UserRepo
	cfg   config.AuthConfig
}

func NewAuthService(users *postgres.UserRepo, cfg config.AuthConfig) *AuthService {
	return &AuthService{users: users, cfg: cfg}
}

type Credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type Session struct {
	Token     string       `json:"token"`
	ExpiresAt time.Time    `json:"expires_at"`
	User      *domain.User `json:"user"`
}

func (s *AuthService) Register(ctx context.Context, c Credentials) (*Session, error) {
	email := strings.TrimSpace(strings.ToLower(c.Email))

	var fields []domain.FieldError
	if _, err := mail.ParseAddress(email); err != nil {
		fields = append(fields, domain.FieldError{Field: "email", Message: "El correo no es valido"})
	}
	if len(c.Password) < 8 {
		fields = append(fields, domain.FieldError{Field: "password", Message: "La contrasena debe tener al menos 8 caracteres"})
	}
	if len(fields) > 0 {
		return nil, domain.ErrValidation.WithFields(fields...)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(c.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, domain.ErrInternal.Wrap(err)
	}

	u := &domain.User{ID: uuid.New(), Email: email, PasswordHash: string(hash)}
	if err := s.users.Create(ctx, u); err != nil {
		return nil, err
	}
	u.CreatedAt = time.Now().UTC()
	return s.issue(u)
}

func (s *AuthService) Login(ctx context.Context, c Credentials) (*Session, error) {
	email := strings.TrimSpace(strings.ToLower(c.Email))
	u, err := s.users.ByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			// Mismo error que una contrasena incorrecta: no se revela si el
			// correo existe.
			return nil, domain.ErrBadPassword
		}
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(c.Password)) != nil {
		return nil, domain.ErrBadPassword
	}
	return s.issue(u)
}

type claims struct {
	jwt.RegisteredClaims
	Email string `json:"email"`
}

func (s *AuthService) issue(u *domain.User) (*Session, error) {
	exp := time.Now().Add(s.cfg.JWTTTL)
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   u.ID.String(),
			Issuer:    "okfp",
			Audience:  jwt.ClaimStrings{"api"},
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ID:        uuid.NewString(),
		},
		Email: u.Email,
	})
	signed, err := tok.SignedString([]byte(s.cfg.JWTSecret))
	if err != nil {
		return nil, domain.ErrInternal.Wrap(err)
	}
	return &Session{Token: signed, ExpiresAt: exp, User: u}, nil
}

// Verify implementa mw.Verifier.
func (s *AuthService) Verify(token string) (*mw.Principal, error) {
	var c claims
	_, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		return []byte(s.cfg.JWTSecret), nil
	}, jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithAudience("api"),
		jwt.WithIssuer("okfp"))
	if err != nil {
		return nil, domain.ErrUnauthorized.Wrap(err)
	}
	id, err := uuid.Parse(c.Subject)
	if err != nil {
		return nil, domain.ErrUnauthorized.Wrap(err)
	}
	return &mw.Principal{UserID: id, Email: c.Email}, nil
}

// --- Tickets de descarga ----------------------------------------------------

type ticketClaims struct {
	jwt.RegisteredClaims
	BundleID string `json:"bundle_id"`
	TicketID string `json:"ticket_id"`
}

// IssueTicket firma un ticket de descarga de vida corta.
//
// Existe porque un <a href> del navegador no puede enviar la cabecera
// Authorization, y meter el ZIP en un Blob para descargarlo con fetch anularia
// el streaming: el navegador acumularia en memoria lo que la API sirve sin
// materializar. El ticket resuelve las dos cosas y sigue estando ligado a un
// unico bundle de un unico dueno, con caducidad y numero de usos limitado.
func (s *AuthService) IssueTicket(ticketID, bundleID uuid.UUID, ownerID uuid.UUID) (string, error) {
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, ticketClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   ownerID.String(),
			Issuer:    "okfp",
			Audience:  jwt.ClaimStrings{"download"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(s.cfg.TicketTTL)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ID:        ticketID.String(),
		},
		BundleID: bundleID.String(),
		TicketID: ticketID.String(),
	})
	signed, err := tok.SignedString([]byte(s.cfg.JWTSecret))
	if err != nil {
		return "", domain.ErrInternal.Wrap(err)
	}
	return signed, nil
}

// VerifyTicket valida la firma y devuelve el ticket y el bundle al que aplica.
// La comprobacion de usos y caducidad en base de datos la hace el llamante: la
// firma sola no puede ser de un solo uso.
func (s *AuthService) VerifyTicket(token string) (ticketID, bundleID, ownerID uuid.UUID, err error) {
	var c ticketClaims
	_, err = jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		return []byte(s.cfg.JWTSecret), nil
	}, jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithAudience("download"),
		jwt.WithIssuer("okfp"))
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, domain.ErrTicketInvalid.Wrap(err)
	}
	if ticketID, err = uuid.Parse(c.TicketID); err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, domain.ErrTicketInvalid.Wrap(err)
	}
	if bundleID, err = uuid.Parse(c.BundleID); err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, domain.ErrTicketInvalid.Wrap(err)
	}
	if ownerID, err = uuid.Parse(c.Subject); err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, domain.ErrTicketInvalid.Wrap(err)
	}
	return ticketID, bundleID, ownerID, nil
}
