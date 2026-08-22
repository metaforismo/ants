-- Evidence, governance and audit surfaces: artifacts, policy decisions,
-- budgets, integrations, the immutable audit log, and the event stream.

CREATE TABLE artifacts (
    id               TEXT PRIMARY KEY,
    tenant_id        TEXT NOT NULL REFERENCES tenants(id),
    run_id           TEXT NOT NULL REFERENCES runs(id),
    kind             TEXT NOT NULL,
    digest           TEXT NOT NULL,
    size_bytes       INT NOT NULL CHECK (size_bytes > 0),
    retention        TEXT NOT NULL,
    producer_task_id TEXT,
    created_at       TIMESTAMPTZ NOT NULL
);

CREATE INDEX artifacts_run_idx ON artifacts (tenant_id, run_id);
CREATE INDEX artifacts_digest_idx ON artifacts (tenant_id, digest);

CREATE TABLE policy_decisions (
    id             TEXT PRIMARY KEY,
    tenant_id      TEXT NOT NULL REFERENCES tenants(id),
    run_id         TEXT NOT NULL REFERENCES runs(id),
    task_id        TEXT,
    principal      TEXT NOT NULL,
    action         TEXT NOT NULL,
    resource       TEXT NOT NULL DEFAULT '',
    outcome        TEXT NOT NULL,
    reason         TEXT NOT NULL,
    policy_version TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL
);

CREATE INDEX policy_decisions_run_idx ON policy_decisions (tenant_id, run_id);

CREATE TABLE budgets (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL REFERENCES tenants(id),
    run_id     TEXT NOT NULL REFERENCES runs(id),
    scope      TEXT NOT NULL,
    limits     JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE integration_connections (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL REFERENCES tenants(id),
    provider   TEXT NOT NULL,
    status     TEXT NOT NULL,
    scopes     JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- Reference into the secret store only; material never lands here.
    secret_ref TEXT NOT NULL,
    version    BIGINT NOT NULL CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX integration_tenant_idx ON integration_connections (tenant_id, provider);

-- The audit log is append-only by convention and by grants: the application
-- role receives INSERT/SELECT only.
CREATE TABLE audit_events (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL,
    actor_type    TEXT NOT NULL,
    actor_id      TEXT NOT NULL,
    action        TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id   TEXT NOT NULL,
    result        TEXT NOT NULL,
    trace_id      TEXT NOT NULL DEFAULT '',
    metadata      JSONB NOT NULL DEFAULT '{}'::jsonb,
    at            TIMESTAMPTZ NOT NULL
);

CREATE INDEX audit_tenant_at_idx ON audit_events (tenant_id, at);

-- Event envelope store. Seq is a global monotonic cursor; per-tenant streams
-- are stable subsequence views of it.
CREATE TABLE events (
    seq               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    id                TEXT NOT NULL UNIQUE,
    type              TEXT NOT NULL,
    occurred_at       TIMESTAMPTZ NOT NULL,
    tenant_id         TEXT NOT NULL,
    aggregate_type    TEXT NOT NULL,
    aggregate_id      TEXT NOT NULL,
    aggregate_version BIGINT NOT NULL DEFAULT 0,
    actor_type        TEXT NOT NULL DEFAULT '',
    actor_id          TEXT NOT NULL DEFAULT '',
    trace_id          TEXT NOT NULL DEFAULT '',
    run_id            TEXT NOT NULL DEFAULT '',
    data              JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX events_tenant_seq_idx ON events (tenant_id, seq);
CREATE INDEX events_run_seq_idx ON events (run_id, seq);
