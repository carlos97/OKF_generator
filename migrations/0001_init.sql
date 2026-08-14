-- ---------------------------------------------------------------------------
-- Esquema inicial de la plataforma de conversion documental a bundles OKF.
--
-- Tres invariantes del enunciado se hacen cumplir AQUI, no en el codigo de la
-- aplicacion, porque la base de datos es el unico arbitro fiable cuando hay
-- varias replicas de API y varios workers compitiendo:
--
--   C4  Un bundle invalido no se publica  -> no se crea fila en `bundles`.
--                                            Los hallazgos viven en `jobs`.
--   C5  Aislamiento por propietario       -> owner_id en toda tabla consultable.
--   C6  Ausencia de duplicados            -> indices unicos parciales +
--                                            transiciones CAS sobre `jobs`.
-- ---------------------------------------------------------------------------

CREATE EXTENSION IF NOT EXISTS citext;

-- --- Usuarios --------------------------------------------------------------
CREATE TABLE users (
    id            UUID PRIMARY KEY,
    email         CITEXT      NOT NULL UNIQUE,
    password_hash TEXT        NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- --- Documentos originales -------------------------------------------------
CREATE TABLE documents (
    id          UUID PRIMARY KEY,
    owner_id    UUID        NOT NULL REFERENCES users (id),
    filename    TEXT        NOT NULL,
    format      TEXT        NOT NULL,
    media_type  TEXT        NOT NULL,
    size_bytes  BIGINT      NOT NULL,
    sha256      TEXT        NOT NULL,
    storage_key TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX documents_owner_idx ON documents (owner_id, created_at DESC);

-- --- Trabajos de conversion ------------------------------------------------
CREATE TABLE jobs (
    id          UUID PRIMARY KEY,
    owner_id    UUID NOT NULL REFERENCES users (id),
    -- RESTRICT y no CASCADE: borrar un documento jamas puede destruir un
    -- bundle publicado ni dejar objetos huerfanos e inidentificables en MinIO.
    document_id UUID NOT NULL REFERENCES documents (id) ON DELETE RESTRICT,

    status TEXT NOT NULL CHECK (status IN (
        'queued', 'running', 'canceling', 'succeeded',
        'invalid', 'failed', 'dead', 'canceled'
    )),

    -- attempt lo incrementa UNICAMENTE ScheduleRetry (decision de negocio).
    -- claim_count cuenta reclamaciones (incluidos robos de lease) y es solo
    -- diagnostico: separarlos evita que un worker que muere en bucle agote el
    -- presupuesto de reintentos y deje el trabajo irreclamable para siempre.
    attempt      INT NOT NULL DEFAULT 1,
    claim_count  INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 3,

    -- Lease: un trabajo en 'running' cuyo lease vencio DEBE poder reclamarse.
    -- Es lo que hace verdadera la demostracion de matar al worker a mitad.
    lease_owner      TEXT,
    lease_expires_at TIMESTAMPTZ,

    -- Se marca solo tras el publisher confirm de RabbitMQ. El barredor
    -- republica exclusivamente los trabajos con este campo en NULL, de modo
    -- que un backlog normal no genera ni un solo mensaje extra.
    enqueued_confirmed_at TIMESTAMPTZ,

    cancel_requested BOOLEAN NOT NULL DEFAULT FALSE,

    -- Reintento idempotente: el reintento crea un job NUEVO enlazado al padre,
    -- de modo que la evidencia del intento fallido queda inmutable.
    parent_job_id UUID REFERENCES jobs (id),
    root_job_id   UUID,

    -- Resultado. Los hallazgos viven en el JOB y no en el bundle, porque un
    -- bundle invalido no llega a existir como fila (C4).
    result_class      TEXT CHECK (result_class IN ('valid', 'valid_with_warnings', 'invalid')),
    okf_score         INT,
    okf_grade         TEXT,
    validation_report JSONB,

    error_code    TEXT,
    error_message TEXT,

    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);

CREATE INDEX jobs_owner_idx    ON jobs (owner_id, created_at DESC);
CREATE INDEX jobs_document_idx ON jobs (document_id);

-- Solo el barredor recorre este indice: trabajos encolados cuyo mensaje nunca
-- llego a confirmarse en la cola.
CREATE INDEX jobs_unconfirmed_idx ON jobs (created_at)
    WHERE status = 'queued' AND enqueued_confirmed_at IS NULL;

-- C6, mitad estructural: un documento no puede tener dos conversiones vivas.
-- Aunque la cola entregue el mensaje dos veces, no puede existir un segundo
-- trabajo activo capaz de producir un segundo bundle.
CREATE UNIQUE INDEX jobs_active_per_document_uk ON jobs (document_id)
    WHERE status IN ('queued', 'running', 'canceling');

-- Reintento idempotente: el doble clic en "Reintentar" devuelve el MISMO job
-- (200) en lugar de crear dos hijos.
CREATE UNIQUE INDEX jobs_single_retry_per_parent_uk ON jobs (parent_job_id)
    WHERE parent_job_id IS NOT NULL;

-- --- Bundles publicados ----------------------------------------------------
-- Esta tabla SOLO contiene bundles que superaron la validacion de plataforma.
-- Como todos los endpoints de lectura resuelven contra ella, no existe ninguna
-- ruta lateral (ni siquiera fichero a fichero) para descargar un bundle
-- invalido. Eso es C4 garantizado por construccion y no por un `if`.
CREATE TABLE bundles (
    id           UUID PRIMARY KEY,          -- id = job_id: un identificador menos
    job_id       UUID NOT NULL REFERENCES jobs (id),
    owner_id     UUID NOT NULL REFERENCES users (id),
    document_id  UUID NOT NULL REFERENCES documents (id) ON DELETE RESTRICT,
    prefix       TEXT NOT NULL,
    -- 'promoting' mientras se copian los objetos al prefijo servible.
    -- La descarga exige 'published', asi que un bundle a medio promover
    -- jamas es descargable.
    status       TEXT NOT NULL CHECK (status IN ('promoting', 'published')),
    unit_count   INT    NOT NULL,
    total_bytes  BIGINT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ
);

-- C6, la otra mitad: el reclamo de esta fila ocurre ANTES de copiar objeto
-- alguno al prefijo publicado, de modo que estructuralmente hay un unico
-- escritor. El indice unico no llega tarde.
CREATE UNIQUE INDEX bundles_one_per_job_uk ON bundles (job_id);
CREATE INDEX bundles_owner_idx ON bundles (owner_id, created_at DESC);

-- --- Manifiesto del bundle -------------------------------------------------
CREATE TABLE bundle_files (
    bundle_id  UUID   NOT NULL REFERENCES bundles (id) ON DELETE CASCADE,
    path       TEXT   NOT NULL,
    size_bytes BIGINT NOT NULL,
    sha256     TEXT   NOT NULL,
    seq        INT    NOT NULL,     -- el ORDEN sale de aqui, nunca del listado de MinIO
    PRIMARY KEY (bundle_id, path)
);

CREATE INDEX bundle_files_seq_idx ON bundle_files (bundle_id, seq);

-- --- Trazabilidad del trabajo ----------------------------------------------
-- El orden lo da el IDENTITY: monotonico y libre de carreras. Calcular un
-- `seq` con SELECT max(seq)+1 seria una carrera con dos entregas concurrentes.
CREATE TABLE job_events (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    job_id     UUID        NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    attempt    INT         NOT NULL,
    type       TEXT        NOT NULL,
    detail     JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX job_events_job_idx ON job_events (job_id, id);

-- Las fases irrepetibles se protegen; los eventos informativos (como
-- duplicate_delivery_ignored, que por definicion puede repetirse) no.
CREATE UNIQUE INDEX job_events_phase_uk ON job_events (job_id, attempt, type)
    WHERE type IN ('claimed', 'published', 'failed', 'canceled');

-- --- Tickets de descarga ---------------------------------------------------
-- Un <a href> del navegador no puede enviar la cabecera Authorization y meter
-- el ZIP en un Blob anularia el streaming. El ticket es de un solo uso (o unos
-- pocos), de vida corta y con alcance a un unico bundle de un unico dueno.
CREATE TABLE download_tickets (
    id         UUID PRIMARY KEY,
    bundle_id  UUID        NOT NULL REFERENCES bundles (id) ON DELETE CASCADE,
    owner_id   UUID        NOT NULL REFERENCES users (id),
    uses       INT         NOT NULL DEFAULT 0,
    max_uses   INT         NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX download_tickets_expiry_idx ON download_tickets (expires_at);
