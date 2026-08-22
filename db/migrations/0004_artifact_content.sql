-- Tranche 2: artifact content storage for single-node mode.
--
-- The memory store keeps artifact content inline; PostgreSQL now does too.
-- Content-addressed object storage replaces this column when artifacts grow
-- beyond single-node scale; the digest stays the integrity anchor either way.

ALTER TABLE artifacts
    ADD COLUMN content BYTEA;

COMMENT ON COLUMN artifacts.content IS
    'Raw bytes for single-node storage; NULL only for rows written before this column existed.';
