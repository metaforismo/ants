-- Tranche 3 / PR D1: durable run claims (ADR-0012, part 1).
--
-- One row per run holds its execution lease. Ownership lives here, never in
-- runs.status: claiming, expiry and fencing must not interfere with the run
-- lifecycle state machine. Every acquisition mints a fresh token and bumps
-- generation + attempts, so stale holders are fenced by construction.

CREATE TABLE run_claims (
    tenant_id    TEXT NOT NULL REFERENCES tenants(id),
    run_id       TEXT NOT NULL REFERENCES runs(id),
    status       TEXT NOT NULL DEFAULT 'runnable'
                 CHECK (status IN ('runnable', 'claimed')),
    owner        TEXT NOT NULL DEFAULT '',
    token        TEXT NOT NULL DEFAULT '',
    generation   BIGINT NOT NULL DEFAULT 0 CHECK (generation >= 0),
    attempts     INT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    acquired_at  TIMESTAMPTZ,
    heartbeat_at TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, run_id),
    -- A run is globally unique (runs.id PK), so at most one claim row can
    -- ever exist for it regardless of tenant scoping.
    UNIQUE (run_id),
    -- A claimed row always carries the full fencing credential set and a
    -- live deadline; a runnable row never does.
    CHECK ((status = 'claimed') = (owner <> '')),
    CHECK ((status = 'claimed') = (token <> '')),
    CHECK (status <> 'claimed' OR (expires_at IS NOT NULL AND generation >= 1 AND attempts >= 1))
);

-- Serves the dispatcher predicate: runnable rows first, then expired leases
-- ordered for reclaim.
CREATE INDEX run_claims_dispatch_idx ON run_claims (status, expires_at);
