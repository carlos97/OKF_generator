// Package postgres implementa la persistencia de metadatos.
//
// Regla transversal de este paquete: TODA funcion que lee o modifica un recurso
// de usuario recibe ownerID EN LA FIRMA y lo aplica en el WHERE. No existe
// ningun `GetByID(id)` que devuelva el recurso para que la capa superior
// compare el propietario despues: ese patron es el que se olvida en un handler
// nuevo y abre el agujero de aislamiento. Al estar el filtro en la firma, no se
// puede compilar una consulta sin propietario.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/uniandes-isis4426/okfp/internal/domain"
)

type DB struct {
	pool *pgxpool.Pool
}

// Connect abre el pool de conexiones.
//
// maxConns se aplica aqui y no en el DSN: `pool_max_conns` es una extension de
// pgxpool, y llevarlo en la cadena de conexion la haria inservible para una
// conexion pgx simple (el migrador), porque el servidor rechaza las claves que
// no reconoce.
func Connect(ctx context.Context, dsn string, maxConns int) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("dsn invalido: %w", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = int32(maxConns)
	}
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("crear pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &DB{pool: pool}, nil
}

func (d *DB) Close()                         { d.pool.Close() }
func (d *DB) Pool() *pgxpool.Pool            { return d.pool }
func (d *DB) Ping(ctx context.Context) error { return d.pool.Ping(ctx) }

// mapErr traduce el "no rows" de pgx al 404 uniforme del dominio. Que la
// ausencia de fila y la propiedad ajena produzcan el mismo error es intencional.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}
