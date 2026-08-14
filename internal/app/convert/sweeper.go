package convert

import (
	"context"
	"time"

	"github.com/uniandes-isis4426/okfp/internal/domain"
	"github.com/uniandes-isis4426/okfp/internal/platform/logging"
)

// sweeperLock protege el barredor de ejecutarse en varias replicas a la vez.
const sweeperLock = "okf-sweeper"

// Publisher es lo que el barredor necesita para republicar.
type Publisher interface {
	Publish(ctx context.Context, msg domain.JobMessage) error
}

// RunSweeper recupera trabajos que se quedaron atascados.
//
// Vive en el WORKER y no en la API: con --scale api=3 habria tres barredores
// compitiendo y multiplicando los mensajes. Ademas se protege con un cerrojo de
// aviso de PostgreSQL, de modo que con --scale worker=3 solo uno barre.
//
// Recupera dos situaciones distintas, y ninguna de las dos dispara en
// funcionamiento normal:
//
//  1. Trabajos en cola cuyo mensaje NUNCA se confirmo (enqueued_confirmed_at
//     nulo). Un backlog normal tiene el campo relleno, asi que no se republica
//     nada por el mero hecho de estar esperando un worker libre. Sin esa
//     distincion, el barredor generaria una tormenta de mensajes que en la
//     consola de RabbitMQ pareceria exactamente lo contrario del control del
//     flujo asincrono.
//  2. Trabajos en ejecucion cuyo lease vencio hace rato: su worker murio sin
//     liberar nada y el mensaje se perdio (por ejemplo, muerto por falta de
//     memoria justo despues del ack).
func (s *Service) RunSweeper(ctx context.Context, pub Publisher) {
	log := logging.From(ctx).With("component", "sweeper")

	t := time.NewTicker(s.cfg.Work.SweeperInterval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			locked, err := s.jobs.TryAdvisoryLock(ctx, sweeperLock)
			if err != nil || !locked {
				continue // otra replica esta barriendo
			}
			s.sweepOnce(ctx, log, pub)
			_ = s.jobs.AdvisoryUnlock(ctx, sweeperLock)
		}
	}
}

func (s *Service) sweepOnce(ctx context.Context, log logger, pub Publisher) {
	unconfirmed, err := s.jobs.StaleQueued(ctx, s.cfg.Work.SweeperStaleAfte, 20)
	if err != nil {
		log.Warn("no se pudieron listar los trabajos sin confirmar", "err", err.Error())
	}
	for _, m := range unconfirmed {
		if err := pub.Publish(ctx, m); err != nil {
			log.Warn("no se pudo republicar", "job_id", m.JobID.String(), "err", err.Error())
			continue
		}
		_ = s.jobs.MarkEnqueued(ctx, m.JobID)
		_ = s.jobs.AppendEvent(ctx, m.JobID, m.Attempt, domain.EventRepublishedBySwep,
			map[string]any{"reason": "encolado_sin_confirmar"})
		log.Info("trabajo republicado por el barredor", "job_id", m.JobID.String())
	}

	// El margen extra sobre el lease evita competir con la renovacion normal.
	grace := s.cfg.Work.JobLease
	expired, err := s.jobs.ReclaimExpiredLeases(ctx, grace, 20)
	if err != nil {
		log.Warn("no se pudieron reclamar los leases vencidos", "err", err.Error())
		return
	}
	for _, m := range expired {
		if err := pub.Publish(ctx, m); err != nil {
			log.Warn("no se pudo republicar tras lease vencido",
				"job_id", m.JobID.String(), "err", err.Error())
			continue
		}
		_ = s.jobs.MarkEnqueued(ctx, m.JobID)
		_ = s.jobs.AppendEvent(ctx, m.JobID, m.Attempt, domain.EventRepublishedBySwep,
			map[string]any{"reason": "lease_vencido"})
		log.Info("trabajo recuperado tras lease vencido", "job_id", m.JobID.String())
	}
}
