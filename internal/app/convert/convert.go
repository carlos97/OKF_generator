// Package convert es el caso de uso del worker: procesar un trabajo de la cola.
//
// Este paquete es el UNICO que importa internal/okf, y solo lo importa
// cmd/worker. La comprobacion `go list -deps ./cmd/api | grep internal/okf`
// debe salir vacia: es la garantia estructural de que la conversion no puede
// ejecutarse dentro de la peticion HTTP.
package convert

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/uniandes-isis4426/okfp/internal/adapters/objectstore"
	"github.com/uniandes-isis4426/okfp/internal/adapters/postgres"
	"github.com/uniandes-isis4426/okfp/internal/adapters/queue"
	"github.com/uniandes-isis4426/okfp/internal/config"
	"github.com/uniandes-isis4426/okfp/internal/domain"
	"github.com/uniandes-isis4426/okfp/internal/okf"
	"github.com/uniandes-isis4426/okfp/internal/okf/bundlefs"
	"github.com/uniandes-isis4426/okfp/internal/okf/parse"
	"github.com/uniandes-isis4426/okfp/internal/platform/logging"
)

// Retrier reprograma y archiva trabajos.
type Retrier interface {
	PublishRetry(ctx context.Context, msg domain.JobMessage) error
	PublishDLQ(ctx context.Context, msg domain.JobMessage) error
}

type Service struct {
	jobs    *postgres.JobRepo
	docs    *postgres.DocumentRepo
	bundles *postgres.BundleRepo
	store   *objectstore.Store
	retry   Retrier
	cfg     *config.Config

	workerID string
}

func NewService(
	jobs *postgres.JobRepo, docs *postgres.DocumentRepo, bundles *postgres.BundleRepo,
	store *objectstore.Store, retry Retrier, cfg *config.Config, workerID string,
) *Service {
	return &Service{jobs: jobs, docs: docs, bundles: bundles, store: store,
		retry: retry, cfg: cfg, workerID: workerID}
}

// Handle procesa una entrega de la cola y decide si se confirma o se descarta.
//
// Toda la logica de idempotencia vive en las primeras lineas: reclamar por
// compare-and-swap y, si no se puede, decidir en funcion del estado real.
func (s *Service) Handle(ctx context.Context, msg domain.JobMessage) queue.Decision {
	log := logging.From(ctx).With("job_id", msg.JobID.String(), "worker", s.workerID)

	claim, err := s.jobs.Claim(ctx, msg.JobID, s.workerID, s.cfg.Work.JobLease)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			// La fila NO existe. Puede ser un identificador inventado o la
			// carrera publish/commit. Nunca se hace ack silencioso: eso
			// destruiria el mensaje y perderia el trabajo sin dejar rastro. A la
			// cola de fallos, donde se puede inspeccionar.
			log.Error("entrega de un trabajo que no existe en la base de datos")
			return queue.NackDrop
		}
		log.Error("no se pudo reclamar el trabajo", "err", err.Error())
		return queue.NackDrop
	}

	if !claim.Claimed {
		cur := claim.Job
		// cur puede ser nil si la lectura del estado fallo: no se desreferencia
		// nunca sin comprobarlo, porque este es el camino caliente de la
		// demostracion de reentrega y un panic aqui produce un bucle de caidas.
		if cur == nil {
			log.Warn("entrega duplicada con estado desconocido")
			return queue.Ack
		}
		switch {
		case cur.Status.IsTerminal():
			// Duplicado real: el trabajo ya termino. UN solo efecto final.
			log.Info("entrega duplicada ignorada", "status", string(cur.Status))
			_ = s.jobs.AppendEvent(ctx, cur.ID, cur.Attempt, domain.EventDuplicateIgnored,
				map[string]any{"status": string(cur.Status), "worker": s.workerID})
			return queue.Ack
		default:
			// Otro worker lo tiene con lease vivo.
			log.Info("el trabajo esta siendo procesado por otro worker",
				"lease_owner", cur.LeaseOwner)
			_ = s.jobs.AppendEvent(ctx, cur.ID, cur.Attempt, domain.EventDuplicateIgnored,
				map[string]any{"reason": "lease_activo", "lease_owner": cur.LeaseOwner})
			return queue.Ack
		}
	}

	job := claim.Job
	if job.ClaimCount > 1 {
		log.Warn("el trabajo se ha reclamado varias veces",
			"claim_count", job.ClaimCount)
		_ = s.jobs.AppendEvent(ctx, job.ID, job.Attempt, domain.EventLeaseStolen,
			map[string]any{"claim_count": job.ClaimCount, "worker": s.workerID})
	}

	// Corta el bucle del documento que mata al worker antes de poder
	// clasificarlo: sin este freno, el mensaje se reentregaria indefinidamente.
	if job.ClaimCount > 10 {
		log.Error("demasiadas reclamaciones, se marca como fallido", "claim_count", job.ClaimCount)
		_ = s.jobs.MarkFailed(ctx, job.ID, s.workerID, domain.JobFailed,
			"claim_thrashing", "El trabajo se reclamo repetidamente sin completarse")
		return queue.Ack
	}

	_ = s.jobs.AppendEvent(ctx, job.ID, job.Attempt, domain.EventClaimed,
		map[string]any{"worker": s.workerID, "attempt": job.Attempt})

	return s.process(ctx, log, job)
}

func (s *Service) process(ctx context.Context, log logger, job *domain.Job) queue.Decision {
	// El presupuesto de tiempo del trabajo es independiente del contexto de
	// apagado: un SIGTERM detiene el consumo de mensajes nuevos, pero no aborta
	// a media conversion lo que ya esta en vuelo.
	jobCtx, cancel := context.WithTimeout(ctx, s.cfg.Work.JobTimeout)
	defer cancel()

	// Renovacion del lease mientras se trabaja. Si se pierde, se aborta: otro
	// worker se ha hecho cargo y publicar ahora produciria dos escrituras del
	// mismo prefijo.
	leaseLost := make(chan struct{})
	go s.renewLease(jobCtx, job.ID, leaseLost)

	doc, err := s.docs.GetInternal(jobCtx, job.DocumentID)
	if err != nil {
		return s.fail(ctx, log, job, err)
	}

	// Descarga del original a memoria: el parser de DOCX necesita acceso
	// aleatorio (archive/zip exige io.ReaderAt), asi que se materializa una vez
	// y todos los parsers comparten el mismo buffer.
	raw, err := s.store.GetAll(jobCtx, s.store.BucketOriginals(), doc.StorageKey,
		s.cfg.Limit.MaxUploadBytes+1)
	if err != nil {
		return s.fail(ctx, log, job, err)
	}
	if int64(len(raw)) > s.cfg.Limit.MaxUploadBytes {
		return s.fail(ctx, log, job, domain.PermanentFault("source_too_large",
			"El documento almacenado supera el limite permitido", nil))
	}

	out, err := okf.Convert(jobCtx, okf.Input{
		Source: &parse.Source{
			R: bytes.NewReader(raw), Size: int64(len(raw)),
			Name: doc.Filename, Format: doc.Format, MimeType: doc.MediaType,
		},
		Options: parse.Options{
			MaxBytes:      s.cfg.Limit.ParseMaxBytes,
			MaxUnits:      s.cfg.Limit.MaxUnits,
			AssetsMax:     s.cfg.Limit.AssetsMaxBytes,
			ZipMaxEntries: s.cfg.Limit.ZipMaxEntries,
			ZipMaxRatio:   s.cfg.Limit.ZipMaxRatio,
		},
		BundleID:    job.ID.String(), // bundle_id = job_id: un identificador menos
		JobID:       job.ID.String(),
		Attempt:     job.Attempt,
		WorkerID:    s.workerID,
		CreatedAt:   job.CreatedAt,
		SlowModeMS:  s.cfg.DemoSlowModeMS,
		FaultInject: s.cfg.FaultInject,
		CheckCancel: func(c context.Context) bool {
			v, err := s.jobs.IsCancelRequested(c, job.ID)
			return err == nil && v
		},
	})

	select {
	case <-leaseLost:
		log.Warn("se perdio el lease durante la conversion; se abandona sin publicar")
		return queue.Ack
	default:
	}

	if err != nil {
		if errors.Is(err, okf.ErrCanceled) {
			log.Info("conversion cancelada por peticion del usuario")
			_ = s.jobs.FinishCanceled(ctx, job.ID, s.workerID)
			return queue.Ack
		}
		return s.fail(ctx, log, job, err)
	}

	if s.cfg.DemoSlowModeMS > 0 {
		_ = s.jobs.AppendEvent(ctx, job.ID, job.Attempt, domain.EventDemoSlowMode,
			map[string]any{"ms": s.cfg.DemoSlowModeMS})
	}

	// --- Bundle invalido: NO se publica -------------------------------------
	//
	// No se crea fila en `bundles`. Como todos los endpoints de lectura
	// resuelven contra esa tabla, la ausencia de fila hace estructuralmente
	// imposible descargarlo, tambien fichero a fichero. Los objetos van a
	// cuarentena para diagnostico, sin ningun endpoint que los sirva.
	if !out.Report.Publishable() {
		log.Info("el bundle no supero la validacion; no se publica",
			"errores", len(out.Report.Errors()))

		qPrefix := objectstore.QuarantinePrefix(job.OwnerID.String(), job.ID.String())
		if err := s.upload(ctx, out, qPrefix); err != nil {
			log.Warn("no se pudo guardar el bundle en cuarentena", "err", err.Error())
		}
		if err := s.jobs.MarkInvalid(ctx, job.ID, s.workerID, out.Report); err != nil {
			log.Error("no se pudo registrar el resultado invalido", "err", err.Error())
			return s.finishCanceledOrNack(ctx, job)
		}
		return queue.Ack
	}

	// --- Publicacion transaccional -----------------------------------------
	if err := s.publish(ctx, log, job, out); err != nil {
		return s.fail(ctx, log, job, err)
	}
	return queue.Ack
}

// publish implementa el orden "reclamar antes de promover".
//
// La secuencia es deliberada y es lo que cierra la ausencia de duplicados
// TAMBIEN en el almacenamiento:
//
//  1. Subir el bundle a un prefijo temporal por intento.
//  2. RECLAMAR la fila del bundle (INSERT ... ON CONFLICT DO NOTHING). Si otro
//     intento gano, se limpia el temporal y se termina SIN tocar el prefijo
//     publicado.
//  3. Copiar del temporal al prefijo publicado.
//  4. Marcar publicado y cerrar el trabajo, comprobando filas afectadas.
//
// Si el reclamo se hiciera despues de copiar, el indice unico llegaria tarde:
// impediria la segunda fila pero no la segunda escritura, y el usuario podria
// descargar un ZIP con la mezcla de dos intentos.
func (s *Service) publish(ctx context.Context, log logger, job *domain.Job, out *okf.Output) error {
	tmpPrefix := objectstore.TempPrefix(job.ID.String(), job.Attempt)
	if err := s.upload(ctx, out, tmpPrefix); err != nil {
		return err
	}

	bundle := &domain.Bundle{
		ID:         job.ID,
		JobID:      job.ID,
		OwnerID:    job.OwnerID,
		DocumentID: job.DocumentID,
		Prefix:     objectstore.PublishedPrefix(job.OwnerID.String(), job.ID.String(), uuid.NewString()),
		Status:     domain.BundlePromoting,
		UnitCount:  out.Units,
		TotalBytes: out.FS.TotalBytes(),
	}

	claimed, err := s.bundles.ClaimForPublish(ctx, bundle, s.workerID)
	if err != nil {
		return err
	}
	if !claimed {
		if canceled, err := s.jobs.IsCancelRequested(ctx, job.ID); err == nil && canceled {
			return s.jobs.FinishCanceled(ctx, job.ID, s.workerID)
		}
		log.Info("otro intento ya publico este bundle; se descarta el trabajo duplicado")
		_ = s.store.RemovePrefix(ctx, s.store.BucketBundles(), tmpPrefix)
		_ = s.jobs.AppendEvent(ctx, job.ID, job.Attempt, domain.EventDuplicateIgnored,
			map[string]any{"reason": "bundle_ya_reclamado", "worker": s.workerID})
		return nil
	}

	files := make([]domain.BundleFile, 0, out.FS.Len())
	for i, f := range out.FS.Files() {
		src := tmpPrefix + f.Path
		dst := bundle.Prefix + f.Path
		if err := s.store.Copy(ctx, s.store.BucketBundles(), src, dst); err != nil {
			_ = s.bundles.DeleteClaim(ctx, bundle.ID, s.workerID)
			return err
		}
		files = append(files, domain.BundleFile{
			Path: f.Path, SizeBytes: int64(len(f.Data)),
			SHA256: bundlefs.SHA256(f.Data), Seq: i,
		})
	}

	if err := s.bundles.Publish(ctx, bundle, files, s.workerID, out.Report); err != nil {
		// Si la cancelacion gano la carrera, o el lease se perdio, no se
		// publica: la transaccion lo detecto por filas afectadas.
		log.Warn("no se pudo cerrar la publicacion", "err", err.Error())
		_ = s.bundles.DeleteClaim(ctx, bundle.ID, s.workerID)
		return err
	}

	_ = s.store.RemovePrefix(ctx, s.store.BucketBundles(), tmpPrefix)

	log.Info("bundle publicado",
		"bundle_id", bundle.ID.String(), "ficheros", len(files),
		"unidades", out.Units, "veredicto", string(out.Report.Verdict),
		"okf_score", out.Report.OKFScore, "duracion_ms", out.Elapsed.Milliseconds())
	return nil
}

func (s *Service) upload(ctx context.Context, out *okf.Output, prefix string) error {
	for _, f := range out.FS.Files() {
		ctype := "text/markdown; charset=utf-8"
		if !bytes.HasSuffix([]byte(f.Path), []byte(".md")) {
			ctype = "application/octet-stream"
		}
		if err := s.store.PutBytes(ctx, s.store.BucketBundles(), prefix+f.Path, f.Data, ctype); err != nil {
			return err
		}
	}
	return nil
}

// fail clasifica el error y decide entre reintentar, morir o fallar.
func (s *Service) fail(ctx context.Context, log logger, job *domain.Job, err error) queue.Decision {
	fault := domain.Classify(err)
	log.Error("fallo el procesamiento del trabajo",
		"code", fault.Code, "kind", kindName(fault.Kind), "err", err.Error())

	if fault.Kind == domain.FaultPermanent {
		if err := s.jobs.MarkFailed(ctx, job.ID, s.workerID, domain.JobFailed, fault.Code, fault.Message); err != nil {
			return s.finishCanceledOrNack(ctx, job)
		}
		return queue.Ack
	}

	// Transitorio: se reprograma con retardo pasando por la cola de espera.
	// NUNCA se usa nack con requeue: reencolaria al instante y produciria un
	// bucle de fuego rapido que saturaria al worker y a la base de datos
	// mientras la dependencia caida se recupera.
	scheduled, next, canceled, serr := s.jobs.ScheduleRetry(ctx, job.ID, s.workerID, fault.Code, fault.Message)
	if serr != nil {
		log.Error("no se pudo reprogramar el reintento", "err", serr.Error())
		return queue.NackDrop
	}
	if canceled {
		if err := s.jobs.FinishCanceled(ctx, job.ID, s.workerID); err != nil {
			log.Error("no se pudo cerrar la cancelacion", "err", err.Error())
			return queue.NackDrop
		}
		return queue.Ack
	}

	msg := domain.JobMessage{
		JobID: job.ID, OwnerID: job.OwnerID, DocumentID: job.DocumentID, Attempt: next,
	}

	if !scheduled {
		// Agotados los intentos.
		if err := s.jobs.MarkFailed(ctx, job.ID, s.workerID, domain.JobDead, fault.Code, fault.Message); err != nil {
			return s.finishCanceledOrNack(ctx, job)
		}
		if err := s.retry.PublishDLQ(ctx, msg); err != nil {
			log.Error("no se pudo archivar en la cola de fallos", "err", err.Error())
			return queue.NackDrop
		}
		return queue.Ack
	}

	if err := s.retry.PublishRetry(ctx, msg); err != nil {
		log.Error("no se pudo publicar en la cola de espera", "err", err.Error())
		// El estado queued sin confirmacion es durable y el barredor lo
		// republicara. Confirmar la entrega evita duplicarla en la DLQ.
		return queue.Ack
	}
	log.Info("reintento programado", "siguiente_intento", next)
	return queue.Ack
}

func (s *Service) finishCanceledOrNack(ctx context.Context, job *domain.Job) queue.Decision {
	canceled, err := s.jobs.IsCancelRequested(ctx, job.ID)
	if err != nil || !canceled {
		return queue.NackDrop
	}
	if err := s.jobs.FinishCanceled(ctx, job.ID, s.workerID); err != nil {
		return queue.NackDrop
	}
	return queue.Ack
}

func (s *Service) renewLease(ctx context.Context, jobID uuid.UUID, lost chan<- struct{}) {
	interval := s.cfg.Work.JobLease / 3
	if interval < time.Second {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ok, err := s.jobs.RenewLease(ctx, jobID, s.workerID, s.cfg.Work.JobLease)
			if err != nil {
				continue // transitorio: se reintenta en el siguiente tick
			}
			if !ok {
				select {
				case lost <- struct{}{}:
				default:
				}
				return
			}
		}
	}
}

type logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

func kindName(k domain.FaultKind) string {
	if k == domain.FaultPermanent {
		return "permanente"
	}
	return "transitorio"
}
