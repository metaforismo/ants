# ADR 0010 — Unit of work: context-carried transactions

Status: accepted
Date: 2026-08-22

## Context

Tranche 1 persisted state transitions and emitted their events as separate
store calls. On PostgreSQL that is two commits — a crash between them leaves
a state change with no event, breaking replay, audit, and the UI event
stream. The plan requires transition metadata and outbox entries in the same
commit (section 6.3).

## Decision

A single seam, `ports.Transactor`:

```go
Do(ctx context.Context, fn func(ctx context.Context) error) error
```

- The PostgreSQL adapter carries the `*sql.Tx` inside the context; every
  store method resolves the caller's transaction through it and falls back
  to the pool when none is active. Callers never see sql/pgx types.
- Nesting joins the outer unit (detected by the carried transaction), so
  helper layers can open units freely without savepoint machinery.
- An error or panic rolls back the whole unit; panics are re-raised after
  rollback.
- Isolation is PostgreSQL's default READ COMMITTED. Correctness relies on
  unique constraints (idempotency keys, slugs, event IDs) and compare-and-swap
  version guards (`UPDATE … WHERE version = $expected`), not on repeatable
  reads. Conflicts surface as typed domain conflicts for callers to handle;
  the engine does not blind-retry them because durable run leases (ADR-0012)
  guarantee a single active writer per run.
- The memory adapter implements real units via whole-state snapshot/restore,
  so the behavioral contract — including rollback on error *and* panic, and
  nested-unit joining — is asserted identically against both adapters in
  `internal/store/storetest`.

The orchestration engine wraps every persisted transition plus its event in
one unit (`transitionRun/Thread/Task`, `StartRun`, failure paths, artifact
storage). API handlers wrap resource creation with its creation event.

## Consequences

- Multi-record invariants hold under crashes mid-transition.
- Context-carried transactions are implicit by design: the alternative
  (threading an executor parameter through every port method) was rejected as
  a large signature churn for zero semantic gain. The trade-off — a hidden
  context dependency — is contained by one unexported key per adapter and
  documented here.
- Long-running work must stay out of units; engine stages only ever wrap
  short persist+emit sequences, never sandbox execution or SCM operations.
