-- Run-history pagination needs an append-stable ordering key. The previous
-- order (created_at, id) is NOT append-stable: created_at comes from the
-- service clock and can move backwards (NTP step correction, VM snapshot
-- restore), so a newly inserted run could sort before an already-consumed
-- positional offset — silently duplicating and omitting history entries for
-- readers walking increasing offsets.
--
-- Each run therefore carries a dense per-thread sequence assigned by the
-- store at insert time (mirroring thread_messages.seq). Existing rows are
-- backfilled in the historical (created_at, id) order, which is the order
-- every prior reader observed; from here on the sequence is the only
-- ordering authority and no clock behavior can reshuffle it.

ALTER TABLE runs ADD COLUMN seq BIGINT;

WITH ranked AS (
    SELECT id,
           ROW_NUMBER() OVER (PARTITION BY tenant_id, thread_id
                              ORDER BY created_at ASC, id ASC) AS rn
    FROM runs
)
UPDATE runs SET seq = ranked.rn FROM ranked WHERE runs.id = ranked.id;

ALTER TABLE runs ALTER COLUMN seq SET NOT NULL;

-- Structurally pins what both stores rely on: sequences are dense per thread
-- starting at 1 (the same posture as runs.version CHECK (version > 0)).
ALTER TABLE runs ADD CONSTRAINT runs_seq_positive CHECK (seq >= 1);

-- The sequence replaces creation time as the listing key; the unique index
-- also makes double allocation structurally impossible under concurrency.
DROP INDEX runs_thread_idx;
CREATE UNIQUE INDEX runs_thread_seq_idx ON runs (tenant_id, thread_id, seq);
