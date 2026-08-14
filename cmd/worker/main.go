// Binario del worker.
//
// Es el unico proceso que contiene el conversor. Se escala de forma
// independiente de la API con:
//
//	docker compose up -d --scale worker=3
//
// y para que eso funcione sin ninguna coordinacion adicional basta con dos
// cosas: no publicar puertos y consumir con prefetch=1, de modo que cada worker
// toma un mensaje y no acapara la cola.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/uniandes-isis4426/okfp/internal/adapters/objectstore"
	"github.com/uniandes-isis4426/okfp/internal/adapters/postgres"
	"github.com/uniandes-isis4426/okfp/internal/adapters/queue"
	"github.com/uniandes-isis4426/okfp/internal/app/convert"
	"github.com/uniandes-isis4426/okfp/internal/config"
	"github.com/uniandes-isis4426/okfp/internal/okf/parse"
	"github.com/uniandes-isis4426/okfp/internal/platform/logging"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// El identificador del worker acaba en jobs.lease_owner y en log.md: sirve
	// para demostrar en la sustentacion cual de las replicas tomo cada trabajo.
	host, _ := os.Hostname()
	workerID := fmt.Sprintf("%s-%s", host, uuid.NewString()[:8])

	log := logging.New(cfg.LogLevel).With("service", "worker", "worker", workerID, "version", version)
	log.Info("arrancando el worker",
		"concurrencia", cfg.Work.Concurrency,
		"job_timeout", cfg.Work.JobTimeout.String(),
		"job_lease", cfg.Work.JobLease.String(),
		"pdf", cfg.Work.EnablePDF && parse.PDFAvailable())

	if cfg.Work.EnablePDF && !parse.PDFAvailable() {
		log.Warn("ENABLE_PDF esta activo pero poppler-utils no esta instalado en esta imagen; " +
			"los PDF fallaran con un error permanente")
	}

	// rootCtx solo detiene el CONSUMO de mensajes nuevos.
	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// workCtx sobrevive a la senal de apagado: el trabajo en vuelo debe poder
	// terminar dentro del stop_grace_period. Si se usara rootCtx, cada reinicio
	// de contenedor abortaria conversiones a medias, multiplicaria los intentos
	// y el usuario veria "reintentando 2/3" sin causa aparente.
	workCtx := context.WithoutCancel(rootCtx)
	workCtx = logging.Into(workCtx, log)

	db, err := postgres.Connect(rootCtx, cfg.DB.DSN(), cfg.DB.MaxConns)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer db.Close()

	store, err := objectstore.New(cfg.S3)
	if err != nil {
		return fmt.Errorf("almacenamiento: %w", err)
	}
	if err := store.WaitReady(rootCtx, 60*time.Second); err != nil {
		return fmt.Errorf("almacenamiento: %w", err)
	}

	publisher, err := queue.NewPublisher(cfg.AMQP.URL())
	if err != nil {
		return fmt.Errorf("cola (publicador): %w", err)
	}
	defer publisher.Close()

	consumer, err := queue.NewConsumer(cfg.AMQP.URL(), cfg.AMQP.Prefetch, workerID)
	if err != nil {
		return fmt.Errorf("cola (consumidor): %w", err)
	}
	defer consumer.Close()

	svc := convert.NewService(
		postgres.NewJobRepo(db),
		postgres.NewDocumentRepo(db),
		postgres.NewBundleRepo(db),
		store, publisher, cfg, workerID,
	)

	// El barredor vive aqui, no en la API, y esta protegido por un cerrojo de
	// aviso: con varias replicas solo una barre.
	go svc.RunSweeper(workCtx, publisher)

	log.Info("consumiendo trabajos", "cola", queue.QueueJobs, "prefetch", cfg.AMQP.Prefetch)
	if err := consumer.Consume(rootCtx, workCtx, svc.Handle); err != nil {
		return fmt.Errorf("consumo: %w", err)
	}

	log.Info("worker detenido ordenadamente")
	return nil
}
