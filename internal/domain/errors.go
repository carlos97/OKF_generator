package domain

import (
	"errors"
	"fmt"
	"net"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/minio/minio-go/v7"
)

// Error es el error de dominio tipado. Todo error que cruza hacia la capa HTTP
// pasa por aqui, de modo que el mapeo a codigo de estado y a cuerpo RFC 7807
// ocurre en un unico sitio y no se puede olvidar en un handler nuevo.
type Error struct {
	Code    string // codigo estable de dominio, consumible por el frontend
	Message string // mensaje para el usuario final
	Status  int    // codigo HTTP sugerido
	Fields  []FieldError
	wrapped error
}

type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	if e.wrapped != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.wrapped)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.wrapped }

func (e *Error) Wrap(err error) *Error {
	c := *e
	c.wrapped = err
	return &c
}

func (e *Error) WithFields(f ...FieldError) *Error {
	c := *e
	c.Fields = append(append([]FieldError{}, c.Fields...), f...)
	return &c
}

func (e *Error) WithMessage(msg string) *Error {
	c := *e
	c.Message = msg
	return &c
}

func newErr(code, msg string, status int) *Error {
	return &Error{Code: code, Message: msg, Status: status}
}

// Catalogo de errores de dominio.
//
// ErrNotFound es deliberadamente el UNICO error que se devuelve tanto cuando el
// recurso no existe como cuando pertenece a otro usuario. Responder 403 en el
// segundo caso confirmaria la existencia del recurso ajeno, que es exactamente
// lo que la condicion de aislamiento prohibe ("deniega la operacion sin revelar
// informacion").
var (
	ErrNotFound     = newErr("not_found", "Recurso no encontrado", 404)
	ErrUnauthorized = newErr("unauthorized", "Credenciales ausentes o invalidas", 401)
	ErrForbidden    = newErr("forbidden", "Operacion no permitida", 403)
	ErrConflict     = newErr("conflict", "El recurso esta en un estado que no admite esta operacion", 409)
	ErrValidation   = newErr("validation_error", "La peticion no es valida", 400)
	ErrInternal     = newErr("internal_error", "Error interno del servidor", 500)
	ErrUnavailable  = newErr("service_unavailable", "Servicio temporalmente no disponible", 503)

	ErrEmailTaken   = newErr("email_taken", "Ya existe una cuenta con ese correo", 409)
	ErrBadPassword  = newErr("invalid_credentials", "Correo o contrasena incorrectos", 401)
	ErrEmptyFile    = newErr("empty_file", "El fichero esta vacio", 400)
	ErrFileTooLarge = newErr("file_too_large", "El fichero supera el tamano maximo permitido", 413)
	ErrUnsupported  = newErr("unsupported_format", "Formato de documento no soportado", 415)

	ErrJobActive        = newErr("job_already_active", "Ya hay una conversion en curso para este documento", 409)
	ErrJobNotRetryable  = newErr("job_not_retryable", "El trabajo no esta en un estado reintentable", 409)
	ErrJobNotCancelable = newErr("job_not_cancelable", "El trabajo ya ha terminado", 409)

	ErrBundleNotReady = newErr("bundle_not_available", "No hay un bundle publicado para este trabajo", 404)
	ErrTicketInvalid  = newErr("ticket_invalid", "El ticket de descarga no es valido o ha caducado", 403)
)

// AsError extrae el *Error de una cadena de errores; si no hay ninguno,
// devuelve ErrInternal envolviendo el original.
func AsError(err error) *Error {
	var de *Error
	if errors.As(err, &de) {
		return de
	}
	return ErrInternal.Wrap(err)
}

// --- Clasificacion transitorio / permanente ---------------------------------

// FaultKind decide si un fallo del worker merece reintento.
//
// Un fallo de infraestructura (MinIO reiniciandose, Postgres no disponible) se
// resolvera solo y debe reintentarse. Un documento corrupto se reintentaria
// tres veces con el mismo resultado, gastando recursos y retrasando el
// diagnostico. Lo DESCONOCIDO se trata como transitorio: es el sesgo seguro,
// porque como maximo cuesta tres intentos, mientras que el sesgo contrario
// descarta trabajo recuperable.
type FaultKind int

const (
	FaultTransient FaultKind = iota
	FaultPermanent
)

type Fault struct {
	Kind    FaultKind
	Code    string
	Message string
	Err     error
}

func (f *Fault) Error() string { return fmt.Sprintf("%s (%s): %v", f.Message, f.Code, f.Err) }
func (f *Fault) Unwrap() error { return f.Err }

func PermanentFault(code, msg string, err error) *Fault {
	return &Fault{Kind: FaultPermanent, Code: code, Message: msg, Err: err}
}

func TransientFault(code, msg string, err error) *Fault {
	return &Fault{Kind: FaultTransient, Code: code, Message: msg, Err: err}
}

// Classify inspecciona la cadena de errores y decide si es reintentable.
func Classify(err error) *Fault {
	if err == nil {
		return nil
	}

	var f *Fault
	if errors.As(err, &f) {
		return f
	}

	// Errores de dominio marcados explicitamente como permanentes: el documento
	// nunca va a convertirse por mucho que se reintente.
	var de *Error
	if errors.As(err, &de) {
		switch de.Code {
		case ErrUnsupported.Code, ErrEmptyFile.Code, ErrFileTooLarge.Code, ErrValidation.Code:
			return PermanentFault(de.Code, de.Message, err)
		}
	}

	// Postgres: las violaciones de restriccion son deterministas (clase 23) y
	// reintentar no cambia nada. El resto (conexion, recursos) es transitorio.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if len(pgErr.Code) >= 2 && pgErr.Code[:2] == "23" {
			return PermanentFault("db_constraint", "Violacion de restriccion en la base de datos", err)
		}
		return TransientFault("db_error", "Error de base de datos", err)
	}

	// MinIO: NoSuchKey sobre el original significa que el objeto no existe y no
	// va a aparecer; el resto es infraestructura.
	var mErr minio.ErrorResponse
	if errors.As(err, &mErr) {
		switch mErr.Code {
		case "NoSuchKey", "NoSuchBucket", "InvalidObjectName":
			return PermanentFault("storage_missing", "El objeto no existe en el almacenamiento", err)
		}
		return TransientFault("storage_error", "Error del almacenamiento de objetos", err)
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return TransientFault("network_error", "Error de red", err)
	}

	return TransientFault("unknown_error", "Error no clasificado", err)
}
