// Package domain contiene las entidades y reglas puras del sistema.
//
// No importa nada de infraestructura (ni pgx, ni chi, ni minio) para que tanto
// cmd/api como cmd/worker puedan depender de el sin arrastrar dependencias
// cruzadas. La deteccion de formato vive aqui, y no en internal/okf, porque la
// API la necesita al recibir la subida y NO puede importar el conversor: esa
// separacion es la que hace verdadera la afirmacion "la conversion no se
// ejecuta dentro de la peticion HTTP".
package domain

import (
	"time"

	"github.com/google/uuid"
)

// --- Usuarios ---------------------------------------------------------------

type User struct {
	ID           uuid.UUID `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

// --- Documentos -------------------------------------------------------------

type Document struct {
	ID         uuid.UUID  `json:"id"`
	OwnerID    uuid.UUID  `json:"-"`
	Filename   string     `json:"filename"`
	Format     Format     `json:"format"`
	MediaType  string     `json:"media_type"`
	SizeBytes  int64      `json:"size_bytes"`
	SHA256     string     `json:"sha256"`
	StorageKey string     `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	DeletedAt  *time.Time `json:"-"`
}

// --- Trabajos ---------------------------------------------------------------

// JobStatus es el estado del TRABAJO, que es cosa distinta de la clasificacion
// del RESULTADO (ResultClass). Un trabajo puede terminar correctamente y aun
// asi producir un bundle invalido: son dos ejes y la rubrica los evalua por
// separado.
type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobCanceling JobStatus = "canceling"
	JobSucceeded JobStatus = "succeeded"
	JobInvalid   JobStatus = "invalid" // la conversion fue bien; el bundle no paso la validacion
	JobFailed    JobStatus = "failed"  // error permanente
	JobDead      JobStatus = "dead"    // agotados los reintentos
	JobCanceled  JobStatus = "canceled"
)

// IsTerminal indica si el trabajo ya no puede progresar. Es la comprobacion que
// hace el worker al recibir una entrega: si el estado es terminal, la entrega
// es un duplicado real y se descarta con ack.
func (s JobStatus) IsTerminal() bool {
	switch s {
	case JobSucceeded, JobInvalid, JobFailed, JobDead, JobCanceled:
		return true
	}
	return false
}

// IsRetryable indica si el usuario puede lanzar un reintento sobre este estado.
// Cancelar por error no debe obligar a volver a subir el documento: el original
// sigue en el almacenamiento.
func (s JobStatus) IsRetryable() bool {
	switch s {
	case JobInvalid, JobFailed, JobDead, JobCanceled:
		return true
	}
	return false
}

type Job struct {
	ID         uuid.UUID `json:"id"`
	OwnerID    uuid.UUID `json:"-"`
	DocumentID uuid.UUID `json:"document_id"`
	Status     JobStatus `json:"status"`

	// DocumentFilename es el nombre del fichero que origino el trabajo. Se
	// incluye en la respuesta porque, sin el, la lista de trabajos solo muestra
	// fechas y estados y no hay manera de saber a que documento corresponde cada
	// fila. No se persiste aqui: se resuelve desde documents al leer.
	DocumentFilename string `json:"document_filename,omitempty"`

	Attempt     int `json:"attempt"`
	ClaimCount  int `json:"claim_count"`
	MaxAttempts int `json:"max_attempts"`

	LeaseOwner     string     `json:"-"`
	LeaseExpiresAt *time.Time `json:"-"`

	EnqueuedConfirmedAt *time.Time `json:"-"`
	CancelRequested     bool       `json:"cancel_requested"`

	ParentJobID *uuid.UUID `json:"parent_job_id,omitempty"`
	RootJobID   *uuid.UUID `json:"root_job_id,omitempty"`

	ResultClass      ResultClass       `json:"result_class,omitempty"`
	OKFScore         *int              `json:"okf_score,omitempty"`
	OKFGrade         string            `json:"okf_grade,omitempty"`
	ValidationReport *ValidationReport `json:"validation_report,omitempty"`

	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`

	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`

	BundleID *uuid.UUID `json:"bundle_id,omitempty"`
}

type JobEvent struct {
	ID        int64          `json:"id"`
	JobID     uuid.UUID      `json:"-"`
	Attempt   int            `json:"attempt"`
	Type      string         `json:"type"`
	Detail    map[string]any `json:"detail,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// Tipos de evento. Los cuatro primeros estan protegidos por un indice unico
// parcial (job_id, attempt, type): una reentrega no puede duplicarlos.
const (
	EventQueued            = "queued"
	EventClaimed           = "claimed"
	EventStage             = "stage"
	EventPublished         = "published"
	EventFailed            = "failed"
	EventCanceled          = "canceled"
	EventRetryScheduled    = "retry_scheduled"
	EventDuplicateIgnored  = "duplicate_delivery_ignored"
	EventLeaseStolen       = "lease_stolen"
	EventValidationFailed  = "validation_failed"
	EventDemoSlowMode      = "demo_slow_mode"
	EventCancelRequested   = "cancel_requested"
	EventRepublishedBySwep = "republished_by_sweeper"
)

// --- Bundles ----------------------------------------------------------------

type BundleStatus string

const (
	BundlePromoting BundleStatus = "promoting"
	BundlePublished BundleStatus = "published"
)

type Bundle struct {
	ID          uuid.UUID    `json:"id"`
	JobID       uuid.UUID    `json:"job_id"`
	OwnerID     uuid.UUID    `json:"-"`
	DocumentID  uuid.UUID    `json:"document_id"`
	Prefix      string       `json:"-"`
	Status      BundleStatus `json:"status"`
	UnitCount   int          `json:"unit_count"`
	TotalBytes  int64        `json:"total_bytes"`
	CreatedAt   time.Time    `json:"created_at"`
	PublishedAt *time.Time   `json:"published_at,omitempty"`

	// SourceFilename es el nombre del documento del que salio el bundle. Se
	// incluye en la respuesta para que el visor pueda indicar el origen y
	// nombrar correctamente la descarga del original, sin una segunda peticion.
	SourceFilename string `json:"source_filename,omitempty"`

	Files []BundleFile `json:"files,omitempty"`
}

type BundleFile struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	Seq       int    `json:"seq"`
}

// --- Clasificacion del resultado -------------------------------------------

// ResultClass son los tres veredictos que la rubrica exige distinguir.
type ResultClass string

const (
	ResultValid             ResultClass = "valid"
	ResultValidWithWarnings ResultClass = "valid_with_warnings"
	ResultInvalid           ResultClass = "invalid"
)

type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Axis separa los dos ejes de calidad. La validez de PLATAFORMA es una puerta
// binaria dura que decide SI SE PUBLICA. La conformidad OKF es una medida de
// calidad que NUNCA bloquea. Mezclarlas en un solo numero haria imposible
// demostrar que un bundle puede ser publicable y a la vez de baja calidad OKF,
// que es justo la distincion que pide el enunciado.
type Axis string

const (
	AxisPlatform Axis = "platform"
	AxisOKF      Axis = "okf"
)

type Finding struct {
	Code     string   `json:"code"` // PLT-xxx o OKF-xxx
	Axis     Axis     `json:"axis"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	Path     string   `json:"path,omitempty"`
}

type ValidationReport struct {
	Verdict  ResultClass `json:"verdict"`
	Findings []Finding   `json:"findings"`

	// Conformidad OKF: se calcula SIEMPRE, incluso para bundles invalidos,
	// y se reporta aparte del veredicto.
	OKFScore int    `json:"okf_score"`
	OKFGrade string `json:"okf_grade"`

	RulesEvaluated int `json:"rules_evaluated"`
}

// Publishable es la regla unica y auditable de publicacion: cualquier hallazgo
// ERROR del eje de plataforma bloquea; los WARNING nunca bloquean; ningun
// hallazgo del eje OKF bloquea jamas.
func (r *ValidationReport) Publishable() bool {
	for _, f := range r.Findings {
		if f.Axis == AxisPlatform && f.Severity == SeverityError {
			return false
		}
	}
	return true
}

// Classify deriva el veredicto de los hallazgos.
func (r *ValidationReport) Classify() ResultClass {
	if !r.Publishable() {
		return ResultInvalid
	}
	for _, f := range r.Findings {
		if f.Axis == AxisPlatform && f.Severity == SeverityWarning {
			return ResultValidWithWarnings
		}
	}
	return ResultValid
}

func (r *ValidationReport) Errors() []Finding   { return r.filter(SeverityError) }
func (r *ValidationReport) Warnings() []Finding { return r.filter(SeverityWarning) }

func (r *ValidationReport) filter(s Severity) []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Severity == s {
			out = append(out, f)
		}
	}
	return out
}

// --- Mensaje de la cola -----------------------------------------------------

// JobMessage es lo unico que viaja por RabbitMQ. Lleva la informacion minima
// para que el worker pueda trabajar leyendo de la base de datos y del
// almacenamiento: nada de estado en memoria de la API.
type JobMessage struct {
	JobID      uuid.UUID `json:"job_id"`
	OwnerID    uuid.UUID `json:"owner_id"`
	DocumentID uuid.UUID `json:"document_id"`
	Attempt    int       `json:"attempt"`
}
