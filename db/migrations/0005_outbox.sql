-- Tranche 2: transactional outbox (ADR-0011).
--
-- Rows are inserted in the SAME transaction as the state change they
-- describe; a separate dispatcher leases and delivers them at-least-once.
-- dedup_key makes publishing idempotent under replays.

CREATE TABLE outbox (
    id           TEXT PRIMARY KEY,
    dedup_key    TEXT NOT NULL UNIQUE,
    tenant_id    TEXT NOT NULL,
    envelope     JSONB NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending', 'leased', 'delivered', 'dead')),
    attempts     INT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts INT NOT NULL CHECK (max_attempts BETWEEN 1 AND 100),
    available_at TIMESTAMPTZ NOT NULL,
    leased_by    TEXT NOT NULL DEFAULT '',
    lease_until  TIMESTAMPTZ,
    last_error   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL,
    delivered_at TIMESTAMPTZ
);

CREATE INDEX outbox_dispatch_idx ON outbox (status, available_at);
