package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/uniandes-isis4426/okfp/internal/domain"
)

type BundleRepo struct{ db *DB }

func NewBundleRepo(db *DB) *BundleRepo { return &BundleRepo{db: db} }

// El nombre del documento de origen se trae con una SUBCONSULTA CORRELACIONADA y
// no con un JOIN a proposito: asi la consulta exterior no necesita alias y esta
// lista de columnas sigue siendo valida tal cual en cualquier contexto, incluido
// un RETURNING. Es la misma precaucion que en jobs.go, donde mezclar columnas con
// alias y sin alias provoco un fallo en tiempo de ejecucion.
const bundleCols = `id, job_id, owner_id, document_id, prefix, status,
	unit_count, total_bytes, created_at, published_at,
	(SELECT d.filename FROM documents d WHERE d.id = bundles.document_id) AS source_filename`

func scanBundle(row pgx.Row) (*domain.Bundle, error) {
	var (
		b      domain.Bundle
		source *string
	)
	err := row.Scan(&b.ID, &b.JobID, &b.OwnerID, &b.DocumentID, &b.Prefix,
		&b.Status, &b.UnitCount, &b.TotalBytes, &b.CreatedAt, &b.PublishedAt, &source)
	if err != nil {
		return nil, mapErr(err)
	}
	if source != nil {
		b.SourceFilename = *source
	}
	return &b, nil
}

// ClaimForPublish es el punto de exclusion mutua de la publicacion, y la
// razon por la que C6 se cumple TAMBIEN en el almacenamiento y no solo en la
// base de datos.
//
// Se ejecuta ANTES de copiar un solo objeto al prefijo servible. Si el worker
// anterior murio despues de crear una fila `promoting`, el nuevo dueño del
// lease recupera esa fila y la apunta a un prefijo nuevo. Asi los objetos que
// aun pudiera escribir el worker anterior quedan inaccesibles.
//
// Devuelve claimed=false cuando otro intento ya gano.
func (r *BundleRepo) ClaimForPublish(
	ctx context.Context, b *domain.Bundle, workerID string,
) (claimed bool, err error) {
	tx, err := r.db.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// El lease se valida antes de insertar o recuperar una promocion. Una
	// transaccion que ya perdio el trabajo no puede modificar la fila bundles.
	tag, err := tx.Exec(ctx,
		`UPDATE jobs SET updated_at = now()
		  WHERE id = $1 AND lease_owner = $2 AND status = 'running'`,
		b.JobID, workerID)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}

	var id uuid.UUID
	err = tx.QueryRow(ctx,
		`INSERT INTO bundles (id, job_id, owner_id, document_id, prefix, status, unit_count, total_bytes)
		 VALUES ($1,$2,$3,$4,$5,'promoting',$6,$7)
		 ON CONFLICT (job_id) DO NOTHING
		 RETURNING id`,
		b.ID, b.JobID, b.OwnerID, b.DocumentID, b.Prefix, b.UnitCount, b.TotalBytes,
	).Scan(&id)

	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return false, nil
		}
		return false, err
	}

	if errors.Is(err, pgx.ErrNoRows) {
		var status string
		if err := tx.QueryRow(ctx,
			`SELECT status FROM bundles WHERE job_id = $1 FOR UPDATE`, b.JobID).Scan(&status); err != nil {
			return false, err
		}
		switch domain.BundleStatus(status) {
		case domain.BundlePublished:
			return false, nil
		case domain.BundlePromoting:
			// La fila anterior solo representa una promocion inconclusa. El
			// lease actual y el bloqueo de fila serializan su reemplazo.
			if _, err := tx.Exec(ctx,
				`UPDATE bundles
				    SET prefix = $2, unit_count = $3, total_bytes = $4, published_at = NULL
				  WHERE job_id = $1 AND status = 'promoting'`,
				b.JobID, b.Prefix, b.UnitCount, b.TotalBytes); err != nil {
				return false, err
			}
		default:
			return false, errors.New("estado de bundle desconocido durante la promocion")
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// Publish cierra la publicacion: marca el bundle como publicado, escribe el
// manifiesto y pasa el trabajo a 'succeeded'.
//
// Toda sentencia comprueba RowsAffected: nunca se confirma una transaccion que
// no hizo nada. Si la cancelacion gano la carrera, el UPDATE del trabajo afecta
// 0 filas y la transaccion se deshace sin publicar. Postgres arbitra, no el
// codigo de aplicacion.
func (r *BundleRepo) Publish(
	ctx context.Context, b *domain.Bundle, files []domain.BundleFile,
	workerID string, report *domain.ValidationReport,
) error {
	tx, err := r.db.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	tag, err := tx.Exec(ctx,
		`UPDATE bundles SET status = 'published', published_at = now(),
		                    unit_count = $2, total_bytes = $3
		  WHERE id = $1 AND status = 'promoting'`, b.ID, b.UnitCount, b.TotalBytes)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrConflict.WithMessage("el bundle ya no esta en promocion")
	}

	for _, f := range files {
		if _, err := tx.Exec(ctx,
			`INSERT INTO bundle_files (bundle_id, path, size_bytes, sha256, seq)
			 VALUES ($1,$2,$3,$4,$5)
			 ON CONFLICT (bundle_id, path) DO UPDATE
			   SET size_bytes = EXCLUDED.size_bytes, sha256 = EXCLUDED.sha256, seq = EXCLUDED.seq`,
			b.ID, f.Path, f.SizeBytes, f.SHA256, f.Seq); err != nil {
			return err
		}
	}

	raw, err := marshalReport(report)
	if err != nil {
		return err
	}

	tag, err = tx.Exec(ctx,
		`UPDATE jobs
		    SET status = 'succeeded', result_class = $3, validation_report = $4,
		        okf_score = $5, okf_grade = $6,
		        finished_at = now(), updated_at = now(),
		        lease_owner = NULL, lease_expires_at = NULL
		  WHERE id = $1 AND lease_owner = $2 AND status = 'running'`,
		b.JobID, workerID, string(report.Verdict), raw, report.OKFScore, report.OKFGrade)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// El lease se perdio o el trabajo fue cancelado justo ahora.
		return domain.ErrConflict.WithMessage("el trabajo ya no admite publicacion")
	}

	if err := appendEventTx(ctx, tx, b.JobID, 1, domain.EventPublished,
		map[string]any{"bundle_id": b.ID.String(), "files": len(files)}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteClaim deshace un reclamo cuando la promocion fallo a mitad, para que un
// reintento posterior pueda volver a intentarlo.
func (r *BundleRepo) DeleteClaim(ctx context.Context, bundleID uuid.UUID, workerID string) error {
	_, err := r.db.pool.Exec(ctx,
		`DELETE FROM bundles b
		  USING jobs j
		 WHERE b.id = $1 AND b.job_id = j.id AND b.status = 'promoting'
		   AND j.lease_owner = $2 AND j.status = 'running'`, bundleID, workerID)
	return err
}

// --- Lectura ----------------------------------------------------------------

// GetPublished es la UNICA resolucion de bundle usada por los tres endpoints de
// lectura (metadatos, fichero suelto y descarga). Compartir esta funcion impide
// que un endpoint nuevo olvide alguna de las dos condiciones y abra por
// descuido una ruta lateral a un bundle no publicado o ajeno.
func (r *BundleRepo) GetPublished(ctx context.Context, ownerID, bundleID uuid.UUID) (*domain.Bundle, error) {
	b, err := scanBundle(r.db.pool.QueryRow(ctx,
		`SELECT `+bundleCols+` FROM bundles
		  WHERE id = $1 AND owner_id = $2 AND status = 'published'`, bundleID, ownerID))
	if err != nil {
		return nil, err
	}
	files, err := r.Files(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	b.Files = files
	return b, nil
}

func (r *BundleRepo) Files(ctx context.Context, bundleID uuid.UUID) ([]domain.BundleFile, error) {
	rows, err := r.db.pool.Query(ctx,
		`SELECT path, size_bytes, sha256, seq FROM bundle_files
		  WHERE bundle_id = $1 ORDER BY seq`, bundleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []domain.BundleFile{}
	for rows.Next() {
		var f domain.BundleFile
		if err := rows.Scan(&f.Path, &f.SizeBytes, &f.SHA256, &f.Seq); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (r *BundleRepo) List(ctx context.Context, ownerID uuid.UUID, limit, offset int) ([]domain.Bundle, error) {
	rows, err := r.db.pool.Query(ctx,
		`SELECT `+bundleCols+` FROM bundles
		  WHERE owner_id = $1 AND status = 'published'
		  ORDER BY created_at DESC LIMIT $2 OFFSET $3`, ownerID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []domain.Bundle{}
	for rows.Next() {
		b, err := scanBundle(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// --- Tickets de descarga ----------------------------------------------------

type Ticket struct {
	ID        uuid.UUID
	BundleID  uuid.UUID
	OwnerID   uuid.UUID
	ExpiresAt time.Time
}

func (r *BundleRepo) CreateTicket(ctx context.Context, ownerID, bundleID uuid.UUID, ttl time.Duration, maxUses int) (*Ticket, error) {
	// Emitir el ticket exige que el bundle exista, este publicado y sea del
	// solicitante: el control de acceso ocurre aqui, no en la descarga.
	if _, err := r.GetPublished(ctx, ownerID, bundleID); err != nil {
		return nil, err
	}
	t := &Ticket{ID: uuid.New(), BundleID: bundleID, OwnerID: ownerID, ExpiresAt: time.Now().Add(ttl)}
	_, err := r.db.pool.Exec(ctx,
		`INSERT INTO download_tickets (id, bundle_id, owner_id, max_uses, expires_at)
		 VALUES ($1,$2,$3,$4,$5)`, t.ID, t.BundleID, t.OwnerID, maxUses, t.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// RedeemTicket consume un uso de forma atomica. El CAS sobre uses < max_uses
// evita que dos peticiones simultaneas agoten el contador mas alla del limite.
func (r *BundleRepo) RedeemTicket(ctx context.Context, ticketID, bundleID uuid.UUID) (uuid.UUID, error) {
	var ownerID uuid.UUID
	err := r.db.pool.QueryRow(ctx,
		`UPDATE download_tickets SET uses = uses + 1
		  WHERE id = $1 AND bundle_id = $2 AND expires_at > now() AND uses < max_uses
		 RETURNING owner_id`, ticketID, bundleID).Scan(&ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, domain.ErrTicketInvalid
	}
	return ownerID, err
}

func (r *BundleRepo) PurgeExpiredTickets(ctx context.Context) error {
	_, err := r.db.pool.Exec(ctx, `DELETE FROM download_tickets WHERE expires_at < now() - interval '1 hour'`)
	return err
}
