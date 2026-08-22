-- Execution core: specs, runs, tasks, workspaces. Optimistic concurrency is
-- enforced with version columns; idempotency keys are unique per thread.

CREATE TABLE specs (
    id               TEXT PRIMARY KEY,
    tenant_id        TEXT NOT NULL REFERENCES tenants(id),
    thread_id        TEXT NOT NULL REFERENCES threads(id),
    version          INT NOT NULL CHECK (version >= 1),
    status           TEXT NOT NULL,
    outcome          TEXT NOT NULL,
    assumptions      JSONB NOT NULL DEFAULT '[]'::jsonb,
    requirements     JSONB NOT NULL DEFAULT '[]'::jsonb,
    non_goals        JSONB NOT NULL DEFAULT '[]'::jsonb,
    success_criteria JSONB NOT NULL DEFAULT '[]'::jsonb,
    blockers         JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at       TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, thread_id, version)
);

CREATE TABLE runs (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT NOT NULL REFERENCES tenants(id),
    thread_id       TEXT NOT NULL REFERENCES threads(id),
    spec_id         TEXT REFERENCES specs(id),
    status          TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    task_ids        JSONB NOT NULL DEFAULT '[]'::jsonb,
    report          JSONB,
    principal       TEXT NOT NULL DEFAULT '',
    failure         JSONB,
    version         BIGINT NOT NULL CHECK (version > 0),
    created_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL,
    finished_at     TIMESTAMPTZ,
    -- A key identifies exactly one execution intent per thread: replays must
    -- return the same run, never create a second one.
    UNIQUE (tenant_id, thread_id, idempotency_key)
);

CREATE INDEX runs_thread_idx ON runs (tenant_id, thread_id, created_at);

CREATE TABLE tasks (
    id           TEXT PRIMARY KEY,
    tenant_id    TEXT NOT NULL REFERENCES tenants(id),
    run_id       TEXT NOT NULL REFERENCES runs(id),
    thread_id    TEXT NOT NULL REFERENCES threads(id),
    name         TEXT NOT NULL,
    kind         TEXT NOT NULL,
    status       TEXT NOT NULL,
    depth        INT NOT NULL DEFAULT 0 CHECK (depth >= 0),
    depends_on   JSONB NOT NULL DEFAULT '[]'::jsonb,
    attempts     INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL CHECK (max_attempts BETWEEN 1 AND 10),
    failure      JSONB,
    version      BIGINT NOT NULL CHECK (version > 0),
    created_at   TIMESTAMPTZ NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL
);

CREATE INDEX tasks_run_idx ON tasks (tenant_id, run_id);

CREATE TABLE workspaces (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL REFERENCES tenants(id),
    task_id    TEXT NOT NULL REFERENCES tasks(id),
    run_id     TEXT NOT NULL REFERENCES runs(id),
    driver     TEXT NOT NULL,
    repo_ref   TEXT NOT NULL,
    branch     TEXT NOT NULL,
    base_sha   TEXT NOT NULL DEFAULT '',
    head_sha   TEXT NOT NULL DEFAULT '',
    status     TEXT NOT NULL,
    version    BIGINT NOT NULL CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX workspaces_run_idx ON workspaces (tenant_id, run_id);
