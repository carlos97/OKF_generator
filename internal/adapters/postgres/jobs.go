package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/uniandes-isis4426/okfp/internal/domain"
)

type JobRepo struct{ db *DB }

func NewJobRepo(db *DB) *JobRepo { return &JobRepo{db: db} }

// OutboxMessage es una intencion durable de publicar un trabajo. El mensaje
// se recompone desde jobs: asi nunca se duplica estado de negocio en RabbitMQ.
type OutboxMessage struct {
	ID          uuid.UUID
	Destination string
	Message     domain.JobMessage
}

// Las columnas del trabajo se declaran DOS veces, con y sin alias, y ambas
// listas deben mantener el MISMO ORDEN porque scanJob es comun a las dos.
//
// jobCols lleva el prefijo `j.` y solo sirve donde hay un `FROM jobs j`.
// jobColsBare no lo lleva y es la unica valida en un `UPDATE ... RETURNING`,
// donde no existe ningun alias: usar alli la version con prefijo produce
// `ERROR: missing FROM-clause entry for table "j"` en tiempo de ejecucion, que
// es un fallo que ni la compilacion ni los tests unitarios detectan.
const jobCols = `
	j.id, j.owner_id, j.document_id, j.status, j.attempt, j.claim_count, j.max_attempts,
	j.lease_owner, j.lease_expires_at, j.enqueued_confirmed_at, j.cancel_requested,
	j.parent_job_id, j.root_job_id, j.result_class, j.okf_score, j.okf_grade,
	j.validation_report, j.error_code, j.error_message,
	j.created_at, j.updated_at, j.started_at, j.finished_at`

const jobColsBare = `
	id, owner_id, document_id, status, attempt, claim_count, max_attempts,
	lease_owner, lease_expires_at, enqueued_confirmed_at, cancel_requested,
	parent_job_id, root_job_id, result_class, okf_score, okf_grade,
	validation_report, error_code, error_message,
	created_at, updated_at, started_at, finished_at`

func scanJob(row pgx.Row) (*domain.Job, error) {
	var (
		j          domain.Job
		leaseOwner *string
		resultCls  *string
		okfGrade   *string
		errCode    *string
		errMsg     *string
		report     []byte
	)
	err := row.Scan(&j.ID, &j.OwnerID, &j.DocumentID, &j.Status, &j.Attempt, &j.ClaimCount,
		&j.MaxAttempts, &leaseOwner, &j.LeaseExpiresAt, &j.EnqueuedConfirmedAt,
		&j.CancelRequested, &j.ParentJobID, &j.RootJobID, &resultCls, &j.OKFScore,
		&okfGrade, &report, &errCode, &errMsg,
		&j.CreatedAt, &j.UpdatedAt, &j.StartedAt, &j.FinishedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	if leaseOwner != nil {
		j.LeaseOwner = *leaseOwner
	}
	if resultCls != nil {
		j.ResultClass = domain.ResultClass(*resultCls)
	}
	if okfGrade != nil {
		j.OKFGrade = *okfGrade
	}
	if errCode != nil {
		j.ErrorCode = *errCode
	}
	if errMsg != nil {
		j.ErrorMessage = *errMsg
	}
	if len(report) > 0 {
		var vr domain.ValidationReport
		if err := json.Unmarshal(report, &vr); err == nil {
			j.ValidationReport = &vr
		}
	}
	return &j, nil
}

// --- Lectura ----------------------------------------------------------------

// Get resuelve el trabajo del propietario indicado y adjunta el id del bundle
// publicado si existe. Un trabajo ajeno produce domain.ErrNotFound.
func (r *JobRepo) Get(ctx context.Context, ownerID, id uuid.UUID) (*domain.Job, error) {
	j, err := scanJob(r.db.pool.QueryRow(ctx,
		`SELECT `+jobCols+` FROM jobs j WHERE j.id = $1 AND j.owner_id = $2`, id, ownerID))
	if err != nil {
		return nil, err
	}
	return r.attachBundleID(ctx, j)
}

// GetInternal lo usa el worker, que llega con el id del mensaje de la cola y
// aun no tiene propietario resuelto.
func (r *JobRepo) GetInternal(ctx context.Context, id uuid.UUID) (*domain.Job, error) {
	return scanJob(r.db.pool.QueryRow(ctx,
		`SELECT `+jobCols+` FROM jobs j WHERE j.id = $1`, id))
}

// attachBundleID adjunta el bundle publicado del trabajo, si existe.
//
// El filtro por owner_id es redundante -el trabajo ya se resolvio por
// propietario y bundles.job_id es unico- pero se mantiene a proposito: hace que
// la consulta sea correcta por si misma y no dependa de que quien la invoque
// haya filtrado antes. Cuesta cero y elimina una excepcion en el test de
// aislamiento.
// Adjunta tambien el nombre del documento de origen, porque sin el la lista de
// trabajos solo muestra fechas y estados y no hay forma de saber a que fichero
// corresponde cada fila.
//
// Ambos valores se traen en UNA sola consulta con subconsultas correlacionadas.
// Deliberadamente no se anaden a jobCols: esa lista se comparte con
// jobColsBare, que se usa en el RETURNING de Claim, y la correlacion tendria que
// escribirse distinta en cada caso (j.document_id frente a jobs.document_id),
// rompiendo la equivalencia posicional que garantiza el test de forma de SQL.
func (r *JobRepo) attachBundleID(ctx context.Context, j *domain.Job) (*domain.Job, error) {
	var (
		bid      *uuid.UUID
		filename *string
	)
	err := r.db.pool.QueryRow(ctx,
		`SELECT
		   (SELECT b.id FROM bundles b
		     WHERE b.job_id = $1 AND b.owner_id = $2 AND b.status = 'published'),
		   (SELECT d.filename FROM documents d WHERE d.id = $3)`,
		j.ID, j.OwnerID, j.DocumentID).Scan(&bid, &filename)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if bid != nil {
		j.BundleID = bid
	}
	if filename != nil {
		j.DocumentFilename = *filename
	}
	return j, nil
}

func (r *JobRepo) List(ctx context.Context, ownerID uuid.UUID, limit, offset int) ([]domain.Job, error) {
	rows, err := r.db.pool.Query(ctx,
		`SELECT `+jobCols+` FROM jobs j WHERE j.owner_id = $1
		  ORDER BY j.created_at DESC LIMIT $2 OFFSET $3`, ownerID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []domain.Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if _, err := r.attachBundleID(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// --- Creacion ---------------------------------------------------------------

// CreateWithDocument inserta documento y trabajo en UNA transaccion.
//
// El orden importa y no es negociable: el COMMIT de esta transaccion ocurre
// SIEMPRE antes de publicar el mensaje en la cola. Publicar primero abre una
// carrera real (el worker consume en milisegundos cuando esta ocioso) en la que
// el worker recibe un job_id que aun no existe en la base de datos; si ademas
// lo tratase como duplicado y lo descartase con ack, el trabajo se perderia en
// silencio y de forma intermitente.
func (r *JobRepo) CreateWithDocument(ctx context.Context, d *domain.Document, j *domain.Job) error {
	tx, err := r.db.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op si ya se hizo commit

	if _, err := tx.Exec(ctx,
		`INSERT INTO documents (id, owner_id, filename, format, media_type, size_bytes, sha256, storage_key)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		d.ID, d.OwnerID, d.Filename, d.Format, d.MediaType, d.SizeBytes, d.SHA256, d.StorageKey,
	); err != nil {
		return err
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO jobs (id, owner_id, document_id, status, attempt, max_attempts, root_job_id)
		 VALUES ($1,$2,$3,'queued',1,$4,$1)`,
		j.ID, j.OwnerID, j.DocumentID, j.MaxAttempts)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" &&
			pgErr.ConstraintName == "jobs_active_per_document_uk" {
			return domain.ErrJobActive
		}
		return err
	}

	if err := appendEventTx(ctx, tx, j.ID, 1, domain.EventQueued, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CreateRetry crea el trabajo hijo de un reintento.
//
// Se crea un job NUEVO en lugar de reutilizar el fallido por dos motivos:
// trazabilidad (los hallazgos y tiempos del intento anterior quedan inmutables
// como evidencia) e idempotencia (un mensaje rezagado del intento anterior
// encuentra su job en estado terminal y se descarta; reutilizando el id seria
// indistinguible del legitimo).
//
// El doble clic en "Reintentar" no crea dos hijos: lo impide el indice unico
// parcial sobre parent_job_id, y en ese caso se devuelve el hijo ya existente.
func (r *JobRepo) CreateRetry(ctx context.Context, ownerID, parentID uuid.UUID) (*domain.Job, error) {
	parent, err := r.Get(ctx, ownerID, parentID)
	if err != nil {
		return nil, err
	}
	if !parent.Status.IsRetryable() {
		return nil, domain.ErrJobNotRetryable
	}

	root := parent.RootJobID
	if root == nil {
		root = &parent.ID
	}

	child := &domain.Job{
		ID:          uuid.New(),
		OwnerID:     ownerID,
		DocumentID:  parent.DocumentID,
		Status:      domain.JobQueued,
		Attempt:     1,
		MaxAttempts: parent.MaxAttempts,
		ParentJobID: &parent.ID,
		RootJobID:   root,
	}

	_, err = r.db.pool.Exec(ctx,
		`INSERT INTO jobs (id, owner_id, document_id, status, attempt, max_attempts, parent_job_id, root_job_id)
		 VALUES ($1,$2,$3,'queued',1,$4,$5,$6)`,
		child.ID, child.OwnerID, child.DocumentID, child.MaxAttempts, child.ParentJobID, child.RootJobID)

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		switch pgErr.ConstraintName {
		case "jobs_single_retry_per_parent_uk":
			// Doble clic: devolver el hijo que ya existe (200, no un error).
			return r.byParent(ctx, ownerID, parent.ID)
		case "jobs_active_per_document_uk":
			return nil, domain.ErrJobActive
		}
	}
	if err != nil {
		return nil, err
	}

	_ = r.AppendEvent(ctx, child.ID, 1, domain.EventQueued,
		map[string]any{"retry_of": parent.ID.String()})
	return child, nil
}

func (r *JobRepo) byParent(ctx context.Context, ownerID, parentID uuid.UUID) (*domain.Job, error) {
	return scanJob(r.db.pool.QueryRow(ctx,
		`SELECT `+jobCols+` FROM jobs j WHERE j.parent_job_id = $1 AND j.owner_id = $2`,
		parentID, ownerID))
}

// --- Encolado ---------------------------------------------------------------

// MarkEnqueued se llama SOLO tras recibir el publisher confirm de RabbitMQ.
// Es lo que permite al barredor distinguir "el publish fallo" de "el trabajo
// espera un worker libre", que es el caso normal con backlog. Sin esta columna
// el barredor republicaria todo trabajo antiguo en cola y generaria una tormenta
// de mensajes que en la consola de RabbitMQ parece justo lo contrario del
// control del flujo asincrono.
func (r *JobRepo) MarkEnqueued(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.pool.Exec(ctx,
		`UPDATE jobs SET enqueued_confirmed_at = now(), updated_at = now() WHERE id = $1`, id)
	return err
}

// ClaimResult es el veredicto del intento de reclamacion.
type ClaimResult struct {
	Claimed bool
	Job     *domain.Job
}

// Claim es la transicion compare-and-swap que sostiene C6.
//
// Reclama el trabajo si esta en cola O si esta en ejecucion con el lease
// vencido. Esa segunda condicion es imprescindible: sin ella, un worker que
// muere a mitad deja el trabajo en 'running' para siempre y la reentrega de
// RabbitMQ se descarta como "duplicado", con lo que el trabajo queda huerfano,
// sin bundle, sin DLQ y sin error visible. Es decir, justo la demostracion de
// tolerancia a fallos fallaria en camara.
//
// attempt NO se incrementa aqui. Contar reclamaciones como intentos hace que un
// worker que muere en bucle agote el presupuesto de reintentos y deje el trabajo
// permanentemente irreclamable. El contador de negocio lo mueve ScheduleRetry.
func (r *JobRepo) Claim(ctx context.Context, id uuid.UUID, workerID string, lease time.Duration) (ClaimResult, error) {
	row := r.db.pool.QueryRow(ctx,
		`UPDATE jobs
		    SET status = 'running',
		        lease_owner = $2,
		        lease_expires_at = now() + $3::interval,
		        claim_count = claim_count + 1,
		        started_at = COALESCE(started_at, now()),
		        updated_at = now()
		  WHERE id = $1
		    AND ((status = 'queued' AND available_at <= now())
		      OR (status = 'running' AND lease_expires_at < now()))
		 RETURNING `+jobColsBare, id, workerID, lease.String())

	j, err := scanJob(row)
	if errors.Is(err, domain.ErrNotFound) {
		// No se pudo reclamar: hay que leer el estado real para decidir si la
		// entrega es un duplicado, si otro worker lo tiene, o si la fila no
		// existe (carrera publish/commit o job_id inventado).
		cur, gerr := r.GetInternal(ctx, id)
		if gerr != nil {
			return ClaimResult{}, gerr
		}
		return ClaimResult{Claimed: false, Job: cur}, nil
	}
	if err != nil {
		return ClaimResult{}, err
	}
	return ClaimResult{Claimed: true, Job: j}, nil
}

// RenewLease mantiene vivo el lease mientras el worker trabaja. Devuelve false
// si el lease ya no es nuestro (otro worker lo robo): en ese caso el trabajo en
// curso debe abortarse sin publicar nada.
func (r *JobRepo) RenewLease(ctx context.Context, id uuid.UUID, workerID string, lease time.Duration) (bool, error) {
	tag, err := r.db.pool.Exec(ctx,
		`UPDATE jobs SET lease_expires_at = now() + $3::interval, updated_at = now()
		  WHERE id = $1 AND lease_owner = $2 AND status IN ('running','canceling')`,
		id, workerID, lease.String())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// IsCancelRequested lo consulta el worker en los puntos de control del pipeline.
func (r *JobRepo) IsCancelRequested(ctx context.Context, id uuid.UUID) (bool, error) {
	var v bool
	err := r.db.pool.QueryRow(ctx, `SELECT cancel_requested FROM jobs WHERE id = $1`, id).Scan(&v)
	return v, mapErr(err)
}

// --- Transiciones finales ---------------------------------------------------

// RequestCancel marca la peticion de cancelacion.
//
// Un mensaje ya entregado a un worker no puede des-encolarse desde AMQP, asi
// que la unica cancelacion posible es cooperativa: se marca la bandera, el
// worker la consulta entre etapas y aborta limpiamente. Si el trabajo aun esta
// en cola, la transicion a 'canceled' es directa por CAS.
func (r *JobRepo) RequestCancel(ctx context.Context, ownerID, id uuid.UUID) (domain.JobStatus, error) {
	// Caso 1: sigue en cola -> terminar ya.
	tag, err := r.db.pool.Exec(ctx,
		`UPDATE jobs SET status = 'canceled', finished_at = now(), updated_at = now()
		  WHERE id = $1 AND owner_id = $2 AND status = 'queued'`, id, ownerID)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() > 0 {
		_ = r.AppendEvent(ctx, id, 1, domain.EventCanceled, map[string]any{"phase": "queued"})
		return domain.JobCanceled, nil
	}

	// Caso 2: en ejecucion -> cancelacion cooperativa.
	tag, err = r.db.pool.Exec(ctx,
		`UPDATE jobs SET cancel_requested = TRUE, status = 'canceling', updated_at = now()
		  WHERE id = $1 AND owner_id = $2 AND status = 'running'`, id, ownerID)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() > 0 {
		_ = r.AppendEvent(ctx, id, 1, domain.EventCancelRequested, nil)
		return domain.JobCanceling, nil
	}

	// Ni una cosa ni la otra: o no existe/es ajeno, o ya termino.
	j, err := r.Get(ctx, ownerID, id)
	if err != nil {
		return "", err
	}
	if j.Status.IsTerminal() {
		return "", domain.ErrJobNotCancelable
	}
	return j.Status, nil
}

// FinishCanceled cierra un trabajo cuya cancelacion cooperativa se atendio.
func (r *JobRepo) FinishCanceled(ctx context.Context, id uuid.UUID, workerID string) error {
	tag, err := r.db.pool.Exec(ctx,
		`UPDATE jobs SET status = 'canceled', finished_at = now(), updated_at = now(),
		                 lease_owner = NULL, lease_expires_at = NULL
		  WHERE id = $1 AND lease_owner = $2 AND status = 'canceling'`, id, workerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrConflict.WithMessage("el trabajo ya no admite cancelacion")
	}
	_ = r.AppendEvent(ctx, id, 1, domain.EventCanceled, nil)
	return nil
}

// MarkInvalid registra un bundle que NO supero la validacion.
//
// Deliberadamente NO se crea fila en `bundles`. Como todos los endpoints de
// lectura del bundle resuelven contra esa tabla, la ausencia de fila hace
// estructuralmente imposible descargarlo, incluso fichero a fichero. Los
// hallazgos siguen visibles en el trabajo, que es lo que el usuario necesita
// para diagnosticar.
func (r *JobRepo) MarkInvalid(ctx context.Context, id uuid.UUID, workerID string, report *domain.ValidationReport) error {
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	tag, err := r.db.pool.Exec(ctx,
		`UPDATE jobs
		    SET status = 'invalid', result_class = $3, validation_report = $4,
		        okf_score = $5, okf_grade = $6,
		        finished_at = now(), updated_at = now(),
		        lease_owner = NULL, lease_expires_at = NULL
		  WHERE id = $1 AND lease_owner = $2 AND status = 'running' AND NOT cancel_requested`,
		id, workerID, string(domain.ResultInvalid), raw, report.OKFScore, report.OKFGrade)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrConflict.WithMessage("el lease del trabajo ya no es nuestro")
	}
	_ = r.AppendEvent(ctx, id, 1, domain.EventValidationFailed,
		map[string]any{"errors": len(report.Errors())})
	return nil
}

// ScheduleRetry devuelve el trabajo a la cola tras un fallo transitorio. Es el
// UNICO sitio donde se incrementa attempt. Una cancelacion que gano la carrera
// se informa por separado y nunca se transforma de nuevo en queued.
func (r *JobRepo) ScheduleRetry(ctx context.Context, id uuid.UUID, workerID, code, msg string) (scheduled bool, next int, canceled bool, err error) {
	var attempt, maxAttempts int
	var status domain.JobStatus
	tx, err := r.db.pool.Begin(ctx)
	if err != nil {
		return false, 0, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op tras Commit

	err = tx.QueryRow(ctx,
		`SELECT status, attempt, max_attempts FROM jobs WHERE id = $1 FOR UPDATE`, id).Scan(&status, &attempt, &maxAttempts)
	if err != nil {
		return false, 0, false, mapErr(err)
	}
	if status == domain.JobCanceling || status == domain.JobCanceled {
		return false, attempt, true, nil
	}
	if status != domain.JobRunning {
		return false, attempt, false, domain.ErrConflict.WithMessage("el trabajo ya no esta en ejecucion")
	}
	if attempt >= maxAttempts {
		return false, attempt, false, nil
	}

	tag, err := tx.Exec(ctx,
		`UPDATE jobs
		    SET status = 'queued', attempt = attempt + 1,
		        lease_owner = NULL, lease_expires_at = NULL,
		        available_at = now() + interval '30 seconds',
		        enqueued_confirmed_at = NULL,
		        error_code = $3, error_message = $4, updated_at = now()
		  WHERE id = $1 AND lease_owner = $2 AND status = 'running' AND NOT cancel_requested`, id, workerID, code, msg)
	if err != nil {
		return false, attempt, false, err
	}
	if tag.RowsAffected() == 0 {
		var current domain.JobStatus
		if err := tx.QueryRow(ctx, `SELECT status FROM jobs WHERE id = $1`, id).Scan(&current); err != nil {
			return false, attempt, false, mapErr(err)
		}
		if current == domain.JobCanceling || current == domain.JobCanceled {
			return false, attempt, true, nil
		}
		return false, attempt, false, domain.ErrConflict.WithMessage("el lease del trabajo ya no es nuestro")
	}
	next = attempt + 1
	if _, err := tx.Exec(ctx,
		`INSERT INTO job_outbox (id, job_id, destination, message_attempt)
		 VALUES ($1, $2, 'retry', $3)`, uuid.New(), id, next); err != nil {
		return false, attempt, false, err
	}
	if err := appendEventTx(ctx, tx, id, attempt, domain.EventRetryScheduled,
		map[string]any{"code": code, "next_attempt": attempt + 1}); err != nil {
		return false, attempt, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, attempt, false, err
	}
	return true, next, false, nil
}

// PendingOutbox devuelve publicaciones aun no confirmadas por RabbitMQ. El
// barredor posee un advisory lock, por lo que una unica replica las despacha.
func (r *JobRepo) PendingOutbox(ctx context.Context, limit int) ([]OutboxMessage, error) {
	rows, err := r.db.pool.Query(ctx,
		`SELECT o.id, o.destination, j.id, j.owner_id, j.document_id, o.message_attempt
		   FROM job_outbox o
		   JOIN jobs j ON j.id = o.job_id
		  WHERE o.published_at IS NULL
		  ORDER BY o.created_at
		  LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []OutboxMessage
	for rows.Next() {
		var item OutboxMessage
		if err := rows.Scan(&item.ID, &item.Destination, &item.Message.JobID,
			&item.Message.OwnerID, &item.Message.DocumentID, &item.Message.Attempt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// MarkOutboxPublished se llama unicamente despues del publisher confirm.
func (r *JobRepo) MarkOutboxPublished(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.pool.Exec(ctx,
		`UPDATE job_outbox SET published_at = now() WHERE id = $1 AND published_at IS NULL`, id)
	return err
}

// MarkFailed cierra el trabajo con un error definitivo (permanente o agotados
// los reintentos).
func (r *JobRepo) MarkFailed(ctx context.Context, id uuid.UUID, workerID string, status domain.JobStatus, code, msg string) error {
	tag, err := r.db.pool.Exec(ctx,
		`UPDATE jobs SET status = $3, error_code = $4, error_message = $5,
		                 finished_at = now(), updated_at = now(),
		                 lease_owner = NULL, lease_expires_at = NULL
		  WHERE id = $1 AND lease_owner = $2 AND status = 'running' AND NOT cancel_requested`,
		id, workerID, string(status), code, msg)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrConflict.WithMessage("el trabajo ya no admite fallo terminal")
	}
	_ = r.AppendEvent(ctx, id, 1, domain.EventFailed, map[string]any{"code": code})
	return nil
}

// --- Barredor ---------------------------------------------------------------

// StaleQueued devuelve los trabajos encolados cuyo mensaje nunca llego a
// confirmarse. Es la unica red de seguridad que recupera un trabajo cuyo
// mensaje se perdio, y por construccion no puede disparar sobre un backlog
// normal.
func (r *JobRepo) StaleQueued(ctx context.Context, olderThan time.Duration, limit int) ([]domain.JobMessage, error) {
	rows, err := r.db.pool.Query(ctx,
		`SELECT id, owner_id, document_id, attempt FROM jobs
		  WHERE status = 'queued' AND enqueued_confirmed_at IS NULL
		    AND available_at <= now()
		    AND NOT EXISTS (
		        SELECT 1 FROM job_outbox o
		         WHERE o.job_id = jobs.id AND o.published_at IS NULL
		    )
		    AND created_at < now() - $1::interval
		  ORDER BY created_at LIMIT $2`, olderThan.String(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.JobMessage
	for rows.Next() {
		var m domain.JobMessage
		if err := rows.Scan(&m.JobID, &m.OwnerID, &m.DocumentID, &m.Attempt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ReclaimExpiredLeases devuelve a la cola los trabajos cuyo worker murio sin
// liberar el lease. El margen extra sobre lease_expires_at evita competir con
// la renovacion normal.
func (r *JobRepo) ReclaimExpiredLeases(ctx context.Context, grace time.Duration, limit int) ([]domain.JobMessage, error) {
	rows, err := r.db.pool.Query(ctx,
		`UPDATE jobs
		    SET status = 'queued', lease_owner = NULL, lease_expires_at = NULL,
		        enqueued_confirmed_at = NULL, updated_at = now()
		  WHERE id IN (
		      SELECT id FROM jobs
		       WHERE status = 'running'
		         AND lease_expires_at < now() - $1::interval
		       ORDER BY lease_expires_at LIMIT $2
		  )
		 RETURNING id, owner_id, document_id, attempt`, grace.String(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.JobMessage
	for rows.Next() {
		var m domain.JobMessage
		if err := rows.Scan(&m.JobID, &m.OwnerID, &m.DocumentID, &m.Attempt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CancelExpiredLeases termina cancelaciones cuyo worker murio antes de atender
// el punto de control. Nunca se reencolan: respetar la intencion del usuario
// es mas importante que recuperar trabajo ya cancelado.
func (r *JobRepo) CancelExpiredLeases(ctx context.Context, grace time.Duration, limit int) ([]uuid.UUID, error) {
	rows, err := r.db.pool.Query(ctx,
		`UPDATE jobs
		    SET status = 'canceled', finished_at = now(), updated_at = now(),
		        lease_owner = NULL, lease_expires_at = NULL
		  WHERE id IN (
		      SELECT id FROM jobs
		       WHERE status = 'canceling'
		         AND lease_expires_at < now() - $1::interval
		       ORDER BY lease_expires_at LIMIT $2
		  )
		 RETURNING id`, grace.String(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// TryAdvisoryLock evita que varias replicas ejecuten el barredor a la vez.
func (r *JobRepo) TryAdvisoryLock(ctx context.Context, key string) (bool, error) {
	var ok bool
	err := r.db.pool.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext($1))`, key).Scan(&ok)
	return ok, err
}

func (r *JobRepo) AdvisoryUnlock(ctx context.Context, key string) error {
	_, err := r.db.pool.Exec(ctx, `SELECT pg_advisory_unlock(hashtext($1))`, key)
	return err
}

// --- Eventos ----------------------------------------------------------------

func (r *JobRepo) AppendEvent(ctx context.Context, jobID uuid.UUID, attempt int, typ string, detail map[string]any) error {
	var raw []byte
	if detail != nil {
		raw, _ = json.Marshal(detail)
	}
	_, err := r.db.pool.Exec(ctx,
		`INSERT INTO job_events (job_id, attempt, type, detail) VALUES ($1,$2,$3,$4)
		 ON CONFLICT DO NOTHING`, jobID, attempt, typ, raw)
	return err
}

func appendEventTx(ctx context.Context, tx pgx.Tx, jobID uuid.UUID, attempt int, typ string, detail map[string]any) error {
	var raw []byte
	if detail != nil {
		raw, _ = json.Marshal(detail)
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO job_events (job_id, attempt, type, detail) VALUES ($1,$2,$3,$4)
		 ON CONFLICT DO NOTHING`, jobID, attempt, typ, raw)
	return err
}

func (r *JobRepo) Events(ctx context.Context, ownerID, jobID uuid.UUID) ([]domain.JobEvent, error) {
	// La subconsulta con owner_id impide leer la traza de un trabajo ajeno.
	rows, err := r.db.pool.Query(ctx,
		`SELECT e.id, e.job_id, e.attempt, e.type, e.detail, e.created_at
		   FROM job_events e
		   JOIN jobs j ON j.id = e.job_id
		  WHERE e.job_id = $1 AND j.owner_id = $2
		  ORDER BY e.id`, jobID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []domain.JobEvent{}
	for rows.Next() {
		var (
			e   domain.JobEvent
			raw []byte
		)
		if err := rows.Scan(&e.ID, &e.JobID, &e.Attempt, &e.Type, &raw, &e.CreatedAt); err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &e.Detail)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
