// Binario de la API.
//
// Este proceso NO convierte documentos. No importa internal/okf ni
// internal/app/convert, y eso puede verificarse con:
//
//	go list -deps ./cmd/api | grep internal/okf     # debe salir vacio
//
// Tampoco escribe en el sistema de ficheros: la subida va en streaming del
// socket al almacenamiento de objetos, de modo que el contenedor puede
// declararse sin volumenes y matarse en cualquier momento sin perder trabajos.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/uniandes-isis4426/okfp/internal/adapters/httpapi"
	"github.com/uniandes-isis4426/okfp/internal/adapters/objectstore"
	"github.com/uniandes-isis4426/okfp/internal/adapters/postgres"
	"github.com/uniandes-isis4426/okfp/internal/adapters/queue"
	"github.com/uniandes-isis4426/okfp/internal/app/apiapp"
	"github.com/uniandes-isis4426/okfp/internal/config"
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

	log := logging.New(cfg.LogLevel)
	log = log.With("service", "api", "version", version)
	log.Info("arrancando la API", "env", cfg.AppEnv, "addr", cfg.HTTPAddr)

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// --- dependencias -------------------------------------------------------
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
		return fmt.Errorf("cola: %w", err)
	}
	defer publisher.Close()

	users := postgres.NewUserRepo(db)
	docs := postgres.NewDocumentRepo(db)
	jobs := postgres.NewJobRepo(db)
	bundles := postgres.NewBundleRepo(db)

	auth := apiapp.NewAuthService(users, cfg.Auth)
	docSvc := apiapp.NewDocumentService(docs, jobs, store, cfg.Limit, cfg.Work.MaxAttempts, cfg.Work.EnablePDF)
	jobSvc := apiapp.NewJobService(jobs, publisher)
	bundleSvc := apiapp.NewBundleService(bundles, store, auth, cfg.Auth)

	health := &httpapi.HealthHandler{
		Version: version,
		Checks: map[string]func() error{
			"postgres": func() error {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				return db.Ping(ctx)
			},
			"objectstore": func() error {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				return store.Ping(ctx)
			},
		},
	}

	handler := httpapi.NewRouter(httpapi.Deps{
		Auth: auth, Documents: docSvc, Jobs: jobSvc, Bundles: bundleSvc,
		Health: health, Cfg: cfg, Logger: log,
	})

	srv := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: handler,
		// ReadTimeout se deja en cero a proposito: una subida legitima de 20 MiB
		// desde una conexion lenta tardaria mas que cualquier valor razonable, y
		// cortarla produciria un 500 inexplicable. El limite de TAMANO lo
		// aplica MaxBytesReader, que es la defensa correcta.
		ReadHeaderTimeout: 10 * time.Second,
		// WriteTimeout tambien en cero: un valor distinto cortaria toda descarga
		// larga por diseno, y el sintoma (ZIP truncado siempre en el mismo
		// punto) parece un fallo del streaming. El plazo se gobierna por
		// escritura con http.NewResponseController.
		IdleTimeout: 60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("API escuchando", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-rootCtx.Done():
		log.Info("senal de apagado recibida; cerrando ordenadamente")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("apagado: %w", err)
	}
	log.Info("API detenida")
	return nil
}
