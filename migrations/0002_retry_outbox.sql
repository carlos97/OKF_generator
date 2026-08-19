-- Un reintento no puede depender de que el worker sobreviva entre el UPDATE
-- del job y el publish a RabbitMQ. Esta migracion guarda la intencion de
-- publicacion junto con la transicion de estado; el barredor la despacha con
-- confirm del broker.

ALTER TABLE jobs
    ADD COLUMN available_at TIMESTAMPTZ NOT NULL DEFAULT now();

CREATE INDEX jobs_ready_queued_idx ON jobs (available_at)
    WHERE status = 'queued';

CREATE TABLE job_outbox (
    id              UUID PRIMARY KEY,
    job_id          UUID        NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    destination     TEXT        NOT NULL CHECK (destination IN ('jobs', 'retry', 'dlq')),
    message_attempt INT         NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at    TIMESTAMPTZ,
    UNIQUE (job_id, destination, message_attempt)
);

CREATE INDEX job_outbox_pending_idx ON job_outbox (created_at)
    WHERE published_at IS NULL;
