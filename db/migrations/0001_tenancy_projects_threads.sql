-- Tenancy roots and project/thread aggregates. Every tenant-scoped table
-- carries tenant_id from day one (ADR-0004); foreign keys keep ownership
-- explicit and deletion cascades deliberate.

CREATE TABLE tenants (
    id          TEXT PRIMARY KEY,
    slug        TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    plan        TEXT NOT NULL,
    region      TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL,
    version     BIGINT NOT NULL CHECK (version > 0),
    created_at  TIMESTAMPTZ NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL
);

CREATE TABLE projects (
    id             TEXT PRIMARY KEY,
    tenant_id      TEXT NOT NULL REFERENCES tenants(id),
    slug           TEXT NOT NULL,
    name           TEXT NOT NULL,
    default_branch TEXT NOT NULL,
    seed_name      TEXT NOT NULL DEFAULT '',
    version        BIGINT NOT NULL CHECK (version > 0),
    created_at     TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, slug)
);

CREATE TABLE threads (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL REFERENCES tenants(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    title      TEXT NOT NULL,
    status     TEXT NOT NULL,
    creator_id TEXT NOT NULL,
    version    BIGINT NOT NULL CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX threads_tenant_status_idx ON threads (tenant_id, status);

CREATE TABLE thread_messages (
    thread_id     TEXT NOT NULL REFERENCES threads(id),
    tenant_id     TEXT NOT NULL,
    seq           BIGINT NOT NULL,
    id            TEXT NOT NULL UNIQUE,
    role          TEXT NOT NULL,
    delivery_mode TEXT NOT NULL,
    content       TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (thread_id, seq)
);
