# ADR 0009 — Deterministic memory-first persistence; SQL schema as the contract

Status: accepted
Date: 2026-08-22

## Context

The plan calls for PostgreSQL as the source of truth, but also demands the
vertical slice run with one command and zero paid/external services
(Horizon 0 exit gate), and that tests be deterministic. This environment has
no running Postgres by default (Docker is available but not assumed for every
developer loop).

## Decision

1. **Ports define persistence** (`internal/ports`); implementations live in
   adapters. Tranche 1 ships the complete deterministic memory adapter used by
   dev, demo, and tests.
2. **The SQL migrations are the schema contract**, written now and validated
   against real Postgres 16 (`db/migrations`, applied by a small embedded,
   advisory-locked runner; `scripts/test-postgres.sh` provisions a disposable
   container). The migration runner is hand-written (~150 lines) instead of
   adopting golang-migrate: forward-only files, one bookkeeping table, no
   down-migrations — less surface than the problem needs.
3. **Postgres store adapters are the next persistence tranche**, implemented
   against the same contract suite that already runs on memory. Until then,
   `store.mode: postgres` fails wiring with an explicit "adapter not
   implemented" error rather than silently degrading to memory.
4. sqlc adoption deferred until query volume justifies codegen; hand-written
   queries with explicit scans arrive with the PG adapters.

## Consequences

- `go test ./...` needs nothing but Go; CI has zero service dependencies for
  the main path.
- Schema drift between memory semantics and SQL is caught by shared contract
  tests once the PG adapter exists — those tests are written first.
- The known cost: two stores to maintain. Mitigated by narrow ports and
  clone-on-read isolation rules documented per implementation.
