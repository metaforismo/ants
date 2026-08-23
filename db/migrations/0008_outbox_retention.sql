-- Tranche 3 PR 3.3: outbox retention/GC index support (ADR-0016).
--
-- Retention sweeps delete terminal rows (delivered, discarded) older than
-- their configured horizon — delivered victims first, oldest-terminal-first
-- within each class. Each partial index covers exactly one eligible class in
-- the order the victim scan requests, so bounded rounds never scan live work
-- (pending/leased/dead are outside the predicates by construction). No table
-- columns change.

CREATE INDEX outbox_retention_delivered_idx ON outbox (delivered_at, id)
    WHERE status = 'delivered';
CREATE INDEX outbox_retention_discarded_idx ON outbox (discarded_at, id)
    WHERE status = 'discarded';
