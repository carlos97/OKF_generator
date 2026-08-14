// Package migrations contiene el esquema SQL embebido en el binario y un
// aplicador minimo.
//
// Se descarta golang-migrate deliberadamente: el proyecto necesita exactamente
// "aplicar en orden los .sql que aun no se han aplicado", y una dependencia
// externa para eso anade superficie de fallo (drivers propios, formato de
// nombres, tabla de version con dirty state) sin aportar nada. Al ir embebido
// con go:embed, el servicio one-shot `migrator` no necesita montar volumenes
// ni copiar ficheros: el binario ya lleva el esquema dentro.
package migrations

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5"
)

//go:embed *.sql
var files embed.FS

// Apply ejecuta las migraciones pendientes dentro de una transaccion por
// fichero. Es idempotente: aplicarla dos veces no tiene efecto la segunda.
func Apply(ctx context.Context, conn *pgx.Conn) ([]string, error) {
	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return nil, fmt.Errorf("crear schema_migrations: %w", err)
	}

	names, err := list()
	if err != nil {
		return nil, err
	}

	var applied []string
	for _, name := range names {
		var exists bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, name,
		).Scan(&exists); err != nil {
			return nil, fmt.Errorf("consultar migracion %s: %w", name, err)
		}
		if exists {
			continue
		}

		body, err := files.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("leer migracion %s: %w", name, err)
		}

		tx, err := conn.Begin(ctx)
		if err != nil {
			return nil, fmt.Errorf("abrir transaccion para %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return nil, fmt.Errorf("aplicar migracion %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version) VALUES ($1)`, name,
		); err != nil {
			_ = tx.Rollback(ctx)
			return nil, fmt.Errorf("registrar migracion %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("confirmar migracion %s: %w", name, err)
		}
		applied = append(applied, name)
	}
	return applied, nil
}

func list() ([]string, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // 0001_, 0002_, ... el prefijo numerico define el orden
	return names, nil
}
