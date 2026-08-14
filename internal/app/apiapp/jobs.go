package apiapp

import (
	"context"

	"github.com/google/uuid"

	"github.com/uniandes-isis4426/okfp/internal/adapters/postgres"
	"github.com/uniandes-isis4426/okfp/internal/domain"
	"github.com/uniandes-isis4426/okfp/internal/platform/logging"
)

// JobService atiende consulta de estado, reintento, cancelacion y reinyeccion.
type JobService struct {
	jobs  *postgres.JobRepo
	queue Enqueuer
}

func NewJobService(jobs *postgres.JobRepo, q Enqueuer) *JobService {
	return &JobService{jobs: jobs, queue: q}
}

// JobView es la vista completa del trabajo que consume el frontend.
type JobView struct {
	*domain.Job
	Events []domain.JobEvent `json:"events"`
}

func (s *JobService) Get(ctx context.Context, ownerID, id uuid.UUID) (*JobView, error) {
	j, err := s.jobs.Get(ctx, ownerID, id)
	if err != nil {
		return nil, err
	}
	ev, err := s.jobs.Events(ctx, ownerID, id)
	if err != nil {
		return nil, err
	}
	return &JobView{Job: j, Events: ev}, nil
}

func (s *JobService) List(ctx context.Context, ownerID uuid.UUID, limit, offset int) ([]domain.Job, error) {
	return s.jobs.List(ctx, ownerID, limit, offset)
}

// Retry crea un trabajo hijo enlazado al fallido y lo encola.
//
// Es idempotente por construccion: el indice unico parcial sobre parent_job_id
// impide dos hijos del mismo padre, y en caso de doble pulsacion el repositorio
// devuelve el hijo ya existente en lugar de un error. El usuario ve el mismo
// trabajo, no un mensaje de fallo.
func (s *JobService) Retry(ctx context.Context, ownerID, parentID uuid.UUID) (*domain.Job, error) {
	child, err := s.jobs.CreateRetry(ctx, ownerID, parentID)
	if err != nil {
		return nil, err
	}

	msg := domain.JobMessage{
		JobID: child.ID, OwnerID: ownerID, DocumentID: child.DocumentID, Attempt: 1,
	}
	if err := s.queue.Publish(ctx, msg); err != nil {
		logging.From(ctx).Error("no se pudo encolar el reintento",
			"job_id", child.ID.String(), "err", err.Error())
		// El trabajo queda persistido y el barredor lo recogera.
		return child, nil
	}
	if err := s.jobs.MarkEnqueued(ctx, child.ID); err != nil {
		logging.From(ctx).Warn("no se pudo confirmar el encolado del reintento",
			"job_id", child.ID.String(), "err", err.Error())
	}
	return child, nil
}

// Cancel solicita la cancelacion.
//
// Si el trabajo sigue en cola, la transicion a cancelado es inmediata. Si ya
// esta en ejecucion, la cancelacion es COOPERATIVA: se marca la bandera y el
// worker la atiende en su siguiente punto de control, porque un mensaje ya
// entregado no puede retirarse de la cola.
func (s *JobService) Cancel(ctx context.Context, ownerID, id uuid.UUID) (domain.JobStatus, error) {
	return s.jobs.RequestCancel(ctx, ownerID, id)
}

// Replay reinyecta el mensaje del trabajo en la cola.
//
// Es la herramienta con la que se demuestra en el video que una entrega
// duplicada no produce un segundo bundle. Vive DENTRO del grupo autenticado y
// resuelve el trabajo con el propietario, como cualquier otro endpoint: una
// herramienta de demostracion que no comprobara la propiedad seria un agujero
// de aislamiento en la propia prueba del aislamiento.
func (s *JobService) Replay(ctx context.Context, ownerID, id uuid.UUID, times int) (int, error) {
	j, err := s.jobs.Get(ctx, ownerID, id)
	if err != nil {
		return 0, err
	}
	if times < 1 {
		times = 1
	}
	if times > 5 {
		times = 5
	}

	sent := 0
	for i := 0; i < times; i++ {
		msg := domain.JobMessage{
			JobID: j.ID, OwnerID: j.OwnerID, DocumentID: j.DocumentID, Attempt: j.Attempt,
		}
		if err := s.queue.Publish(ctx, msg); err != nil {
			return sent, err
		}
		sent++
	}
	_ = s.jobs.AppendEvent(ctx, j.ID, j.Attempt, "replayed",
		map[string]any{"times": sent})
	return sent, nil
}
