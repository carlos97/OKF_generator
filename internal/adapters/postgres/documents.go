package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/uniandes-isis4426/okfp/internal/domain"
)

type DocumentRepo struct{ db *DB }

func NewDocumentRepo(db *DB) *DocumentRepo { return &DocumentRepo{db: db} }

const docCols = `id, owner_id, filename, format, media_type, size_bytes, sha256, storage_key, created_at`

// Get resuelve un documento del propietario indicado. Un documento ajeno o
// inexistente producen exactamente el mismo domain.ErrNotFound.
func (r *DocumentRepo) Get(ctx context.Context, ownerID, id uuid.UUID) (*domain.Document, error) {
	var d domain.Document
	err := r.db.pool.QueryRow(ctx,
		`SELECT `+docCols+` FROM documents
		  WHERE id = $1 AND owner_id = $2 AND deleted_at IS NULL`, id, ownerID,
	).Scan(&d.ID, &d.OwnerID, &d.Filename, &d.Format, &d.MediaType,
		&d.SizeBytes, &d.SHA256, &d.StorageKey, &d.CreatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &d, nil
}

// GetInternal lo usa EXCLUSIVAMENTE el worker, que ya ha resuelto la propiedad
// a traves del job. Se mantiene con nombre distinto y comentario explicito para
// que no se cuele por descuido en un handler HTTP.
func (r *DocumentRepo) GetInternal(ctx context.Context, id uuid.UUID) (*domain.Document, error) {
	var d domain.Document
	err := r.db.pool.QueryRow(ctx,
		`SELECT `+docCols+` FROM documents WHERE id = $1`, id,
	).Scan(&d.ID, &d.OwnerID, &d.Filename, &d.Format, &d.MediaType,
		&d.SizeBytes, &d.SHA256, &d.StorageKey, &d.CreatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &d, nil
}

func (r *DocumentRepo) List(ctx context.Context, ownerID uuid.UUID, limit, offset int) ([]domain.Document, error) {
	rows, err := r.db.pool.Query(ctx,
		`SELECT `+docCols+` FROM documents
		  WHERE owner_id = $1 AND deleted_at IS NULL
		  ORDER BY created_at DESC LIMIT $2 OFFSET $3`, ownerID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []domain.Document{}
	for rows.Next() {
		var d domain.Document
		if err := rows.Scan(&d.ID, &d.OwnerID, &d.Filename, &d.Format, &d.MediaType,
			&d.SizeBytes, &d.SHA256, &d.StorageKey, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SoftDelete marca el documento como borrado sin destruir nada.
//
// Un DELETE fisico con ON DELETE CASCADE destruiria los bundles publicados del
// historico y dejaria sus objetos huerfanos en el almacenamiento, sin ninguna
// fila que registre su clave y por tanto sin forma de identificarlos ni
// borrarlos despues.
func (r *DocumentRepo) SoftDelete(ctx context.Context, ownerID, id uuid.UUID) error {
	tag, err := r.db.pool.Exec(ctx,
		`UPDATE documents SET deleted_at = now()
		  WHERE id = $1 AND owner_id = $2 AND deleted_at IS NULL`, id, ownerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
