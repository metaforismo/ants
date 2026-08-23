# ADR 0016 — Outbox retention: bounded garbage collection of terminal delivery rows

Status: accepted
Date: 2026-08-23

## Context

ADR-0011 left every outbox row in place forever: `delivered` rows are pure
delivery bookkeeping that accumulate one-for-one with domain events, and
ADR-0015 made `discarded` an explicit terminal operator decision whose rows
were retained "forever by this feature", deferring "outbox retention/GC"
namingly. The deferral is now the gap: the outbox table grows unboundedly,
dominated by `delivered` rows that no subsystem will ever read again. The
master plan's retention stance (section 2) is "ephemeral working data, audit
durable and configurable"; outbox rows are operational bookkeeping, not audit
— events live durably in `events`, interventions in `audit_events` — so a
reviewed deletion policy for the queue table itself is consistent with the
plan provided it can never touch history tables nor open work.

Deletion is destructive, so the semantics were interrogated against the plan,
ADR-0004/0011/0013/0014/0015, and the existing state machine before any code.

## Decision

### Eligible rows: terminal states only, never open work

One bounded GC round deletes ONLY rows in `delivered` or `discarded`. This is
a hard structural property of the selection predicate, not an operator
promise:

- `pending` / `leased` are open or claimable work — never eligible;
- `dead` is an operator target under compare-and-swap fencing (ADR-0015);
  deleting dead letters would silently destroy both the poison-message
  forensics and the fencing credential operators hold;
- rows with a NULL terminal timestamp (`delivered_at` / `discarded_at`) are
  never eligible — a row that cannot prove when it became terminal survives
  every round (fail-safe for legacy or partially written rows);
- domain events and audit records are never touched by this feature; the GC
  port has no path to any other table.

### Two distinct, independently configured horizons

`delivered_after` and `discarded_after` gate the two eligible classes
separately:

- **Delivered** rows exist only to make at-least-once delivery auditable
  while it is in flight; once acknowledged, the durable event row carries all
  semantic content. Forensic value is low → shorter horizon is expected.
- **Discarded** rows record an explicit operator triage of a poison message,
  referenced by a versioned event (`outbox.dead_letter.discarded.v1`) and a
  tenant-scoped audit record. They carry incident-forensics value → longer
  horizon is expected.

A row becomes collectable once its terminal age reaches its class horizon:
`terminal_at <= cutoff - horizon` measured against the store's clock. The
boundary is inclusive by design — a row is guaranteed to survive *at least*
its horizon, pinned by contract tests at exactly-horizon age.

### Inert unless intentionally configured

Defaults keep GC structurally inert: both horizons default to zero, and zero
means "this class is exempt". Nothing is deleted on upgrade, and a deployment
that never configures retention never runs deletion — matching the standing
posture where ADR-0013/0015 deferred retention rather than promising it.
Setting a nonzero horizon IS the intentional act; there is no separate enable
flag to forget or misconfigure. Batch size and sweep interval have validated
defaults so an enabled configuration cannot be accidentally unbounded.

### Store-owned cutoff, durations-only requests

The request carries durations and a limit; the adapter computes ONE cutoff
instant per round from its own injected clock and reports it back in the
result (ADR-0011 invariant: callers never supply scheduling timestamps). No
raw SQL, envelopes, or timestamps cross the port.

### One atomic, class-prioritized, bounded round

A round deletes at most `batch_size` rows total. Budget is allocated
deterministically: delivered victims first in `(delivered_at, id)` order —
oldest-terminal-first within the class — then discarded victims in
`(discarded_at, id)` order with the remaining budget. The ordering is a class
budget with per-class oldest-first consumption, deliberately NOT a global
oldest-first scan across classes: delivery bookkeeping is reclaimable
sooner than operator triage history, so under a tight budget the delivered
class must drain first even when a discarded row is older.
PostgreSQL executes both deletions as two single statements inside one unit
of work; each statement selects its victims with `FOR UPDATE SKIP LOCKED` so
concurrent sweeps, dispatchers, and operator mutations can neither collide
nor observe half-deleted rounds. The memory adapter does the same under its
write lock, and both honor unit-of-work rollback (a rolled-back unit restores
deleted rows). Reruns are idempotent: already-deleted rows simply no longer
match. Partial indexes on each terminal class support the victim scans
(forward-only migration 0008).

### Global rounds, not per-tenant

Rounds span all tenants. GC is infrastructure maintenance symmetric with the
dispatcher's own global `Lease`; it returns counts only — no tenant, message,
or payload identifiers ever leave the store through this surface, so there is
no enumeration or anti-tenancy exposure (ADR-0004 governs tenant-visible
reads, which this is not). Per-tenant retention policy (plan section 6.1)
belongs to the managed-cloud era and stays deferred.

### Scheduling: lifecycle loop when configured, plus an explicit CLI

Both surfaces share one service seam performing one round:

- `serve` starts a GC loop only when at least one horizon is nonzero, on a
  validated interval (default 1h), and stops it FIRST during graceful
  shutdown — destructive maintenance must not compete with worker/outbox
  drain phases, and stopping it early leaves at most a skipped round, never
  partial state (each round is atomic).
- `ants outbox retention preview|sweep` gives operators on-demand control.
  `preview` never deletes (it runs the identical selection AND budget
  allocation with mutation disabled, so previews cannot promise more than a
  bounded round would delete, and preview and sweep cannot drift). `sweep`
  requires
  `--yes`: without it the command prints what would be deleted and exits with
  usage status — no interactive prompt exists, so automation can never hang,
  and tests prove zero rows are deleted when the flag is omitted.

### Metrics: reviewed amendment to the ADR-0014 closed set

One counter family joins via the established consumer-side observer seam:
`ants_outbox_retention_deleted_total{state}` with state ∈ {delivered,
discarded}, plus `ants_outbox_retention_rounds_total`. Labels are fixed
vocabularies; tenant, message, and principal identifiers are never labels.
Observers run inline after persistence outcomes are known and cannot alter
behavior (nil observer = identical behavior).

## Alternatives considered

- **Delete-on-discard** — rejected again (ADR-0015): discard records triage;
  retention policy decides deletion, and now does so explicitly.
- **Collect dead letters after a horizon** — rejected: dead letters are open
  operator work; auto-deleting them would race human incident response and
  destroy fencing credentials. If demand emerges it gets its own reviewed
  horizon, not a silent default.
- **Per-tenant retention configuration now** — rejected: no authenticated
  tenant-facing surface exists to configure it; inventing one exceeds this
  tranche's scope.
- **Separate enable flag beside horizons** — rejected: two knobs expressing
  one intent invites the "enabled but zero horizons" dead configuration; the
  horizon itself is the switch.
- **Time-based cleanup inside the dispatcher loop** — rejected: mixing
  destructive maintenance into the delivery hot path makes the dispatcher's
  bounded-round guarantees harder to reason about; a separate seam with its
  own observer and lifecycle keeps blast radius small.

## Consequences

- Deployments get a bounded, reviewable way to reclaim outbox space; the
  unbounded-growth limitation noted since ADR-0011 closes.
- Events, audit history, dead letters, and all nonterminal rows remain
  permanently outside GC reach — pinned by the shared contract suite on both
  adapters.
- Operators gain `ants outbox retention preview/sweep` and, optionally, an
  automatic loop whose activity is visible through two new metric series.
- Deferred unchanged: per-tenant retention policy, collecting dead letters,
  archiving/purging domain events or audit history, multi-node dispatcher
  scale-out, authenticated remote operator APIs.
