// Binario de utilidades. Un solo ejecutable con subcomandos, usado por los
// servicios one-shot de Docker Compose y por el equipo durante la demostracion.
//
//	tools migrate      aplica las migraciones (servicio `migrator`)
//	tools buckets      crea los buckets del almacenamiento (servicio `minio-init`)
//	tools topology     declara exchanges y colas (servicio `topology`)
//	tools seed         crea los usuarios de prueba
//	tools gen-large    genera el documento grande para la evidencia de asincronia
//	tools queue-depth  imprime la profundidad de las colas
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/uniandes-isis4426/okfp/internal/adapters/objectstore"
	"github.com/uniandes-isis4426/okfp/internal/adapters/postgres"
	"github.com/uniandes-isis4426/okfp/internal/adapters/queue"
	"github.com/uniandes-isis4426/okfp/internal/app/apiapp"
	"github.com/uniandes-isis4426/okfp/internal/config"
	"github.com/uniandes-isis4426/okfp/migrations"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cfg, err := config.Load()
	if err != nil {
		fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	switch os.Args[1] {
	case "migrate":
		fatalIf(runMigrate(ctx, cfg))
	case "buckets":
		fatalIf(runBuckets(ctx, cfg))
	case "topology":
		fatalIf(runTopology(cfg))
	case "seed":
		fatalIf(runSeed(ctx, cfg))
	case "gen-large":
		fatalIf(runGenLarge(os.Args[2:]))
	case "queue-depth":
		fatalIf(runQueueDepth(cfg))
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "subcomando desconocido: %s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Print(`tools - utilidades de la plataforma OKF

Uso: tools <subcomando>

  migrate       Aplica las migraciones pendientes.
  buckets       Crea los buckets del almacenamiento de objetos.
  topology      Declara exchanges y colas en RabbitMQ.
  seed          Crea los usuarios de prueba (ana@demo.local / beto@demo.local).
  gen-large     Genera testdata/05-lento-grande.md.
                  --sections N   numero de secciones (por defecto 400)
                  --out PATH     fichero de salida
  queue-depth   Imprime la profundidad de las colas.
`)
}

// runMigrate aplica el esquema. Se ejecuta con una conexion simple (no pool):
// es un proceso one-shot y las migraciones deben ser secuenciales.
func runMigrate(ctx context.Context, cfg *config.Config) error {
	conn, err := waitForPostgres(ctx, cfg.DB.DSN(), 90*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	applied, err := migrations.Apply(ctx, conn)
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		fmt.Println("migraciones: nada que aplicar, el esquema ya esta al dia")
		return nil
	}
	for _, name := range applied {
		fmt.Printf("migraciones: aplicada %s\n", name)
	}
	return nil
}

// waitForPostgres tolera que el contenedor de base de datos aun este
// inicializandose, aunque el healthcheck ya deberia cubrirlo.
func waitForPostgres(ctx context.Context, dsn string, timeout time.Duration) (*pgx.Conn, error) {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		conn, err := pgx.Connect(ctx, dsn)
		if err == nil {
			return conn, nil
		}
		last = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return nil, fmt.Errorf("postgres no disponible: %w", last)
}

func runBuckets(ctx context.Context, cfg *config.Config) error {
	store, err := objectstore.New(cfg.S3)
	if err != nil {
		return err
	}
	if err := store.WaitReady(ctx, 90*time.Second); err != nil {
		return err
	}
	if err := store.EnsureBuckets(ctx); err != nil {
		return err
	}
	fmt.Printf("buckets listos: %s, %s\n", store.BucketOriginals(), store.BucketBundles())
	return nil
}

func runTopology(cfg *config.Config) error {
	var last error
	deadline := time.Now().Add(90 * time.Second)

	for time.Now().Before(deadline) {
		conn, err := queue.Dial(cfg.AMQP.URL())
		if err != nil {
			last = err
			time.Sleep(time.Second)
			continue
		}
		defer conn.Close()

		if err := queue.DeclareTopology(conn); err != nil {
			return err
		}
		fmt.Printf("topologia declarada: %s, %s, %s\n",
			queue.QueueJobs, queue.QueueRetry, queue.QueueDLQ)
		return nil
	}
	return fmt.Errorf("rabbitmq no disponible: %w", last)
}

// runSeed crea dos usuarios. Tener DOS es deliberado: la demostracion del
// aislamiento necesita que un usuario intente acceder al recurso de otro.
func runSeed(ctx context.Context, cfg *config.Config) error {
	db, err := postgres.Connect(ctx, cfg.DB.DSN(), cfg.DB.MaxConns)
	if err != nil {
		return err
	}
	defer db.Close()

	auth := apiapp.NewAuthService(postgres.NewUserRepo(db), cfg.Auth)

	for _, c := range []apiapp.Credentials{
		{Email: "ana@demo.local", Password: "Demo12345"},
		{Email: "beto@demo.local", Password: "Demo12345"},
	} {
		if _, err := auth.Register(ctx, c); err != nil {
			fmt.Printf("seed: %s ya existe o no se pudo crear (%v)\n", c.Email, err)
			continue
		}
		fmt.Printf("seed: creado %s (contrasena Demo12345)\n", c.Email)
	}
	return nil
}

func runQueueDepth(cfg *config.Config) error {
	conn, err := queue.Dial(cfg.AMQP.URL())
	if err != nil {
		return err
	}
	defer conn.Close()

	for _, q := range []string{queue.QueueJobs, queue.QueueRetry, queue.QueueDLQ} {
		n, err := queue.QueueDepth(conn, q)
		if err != nil {
			fmt.Printf("%-16s error: %v\n", q, err)
			continue
		}
		fmt.Printf("%-16s %d mensaje(s)\n", q, n)
	}
	return nil
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}

func fatalIf(err error) {
	if err != nil {
		fatal(err)
	}
}
