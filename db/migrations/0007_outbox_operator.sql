-- Tranche 3 PR 3.2: outbox dead-letter operator tooling (ADR-0015).
--
-- generation is the compare-and-swap credential for operator requeue/discard:
-- it increments on every transition into dead, requeued-pending, or
-- discarded, so a credential read from one epoch can never act on a newer
-- retry or delivery transition.
-- dead_at / discarded_at record when the operator-visible transitions happened;
-- discarded rows are retained in place (retention/GC is deferred).
-- The status check gains 'discarded' as an explicit terminal operator decision.

ALTER TABLE outbox ADD COLUMN generation BIGINT NOT NULL DEFAULT 0;
ALTER TABLE outbox ADD COLUMN dead_at TIMESTAMPTZ;
ALTER TABLE outbox ADD COLUMN discarded_at TIMESTAMPTZ;

-- Rows that died before this migration predate the generation column; they
-- entered dead exactly once, so their epoch starts at 1 and stays operable.
-- Live rows keep generation 0: never having died, they are not operator
-- targets, and a generation-0 credential is structurally invalid.
UPDATE outbox SET generation = 1 WHERE status = 'dead';

ALTER TABLE outbox DROP CONSTRAINT outbox_status_check;
ALTER TABLE outbox ADD CONSTRAINT outbox_status_check
    CHECK (status IN ('pending', 'leased', 'delivered', 'dead', 'discarded'));

CREATE INDEX outbox_dead_letter_idx ON outbox (tenant_id, created_at, id)
    WHERE status = 'dead';
