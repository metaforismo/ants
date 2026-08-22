# ADR 0012 — Durable run claims (part 1: storage aggregate)

Status: accepted (part 1 of 2; worker integration follows in part 2)
Date: 2026-08-22

## Context

Runs execute over many minutes across processes that crash, restart, and
overlap. Today nothing in the data model says *who may execute a run*: the
engine trusts its own single-process lifetime (ADR-0002 accepts this), and
`Run.Status` describes pipeline stage only. Making `Run.Status` do double
duty as an execution lease was rejected: status transitions are a closed,
audited state machine, and bolting owner/expiry semantics onto them would
corrupt both. ADR-0010 already anticipated durable run leases as the reason
the engine must not blind-retry conflicts.

What was needed is a tenant-scoped **run_claims** aggregate with the
semantics of a renewable lease plus fencing tokens:

- at most one live holder per run;
- reclaim after expiry so crashed workers never strand runs;
- stale holders rejected by construction, not by politeness;
- deterministic testability without sleeps.

## Decision (part 1 — persistence and contracts)

**Separate aggregate.** `run_claims` is its own table and port
(`ports.RunClaimStore`, contract `ants.store.runclaims.v1`), keyed by
`(tenant_id, run_id)` with `UNIQUE (run_id)`. States are exactly
`runnable ⇄ claimed`; terminality is deletion of the row, never a third
status, so recovery cannot resurrect finished work. `Run.Status` is never
overloaded.

**Fencing credentials.** Every successful acquisition mints a fresh opaque
bearer token (`crypto/rand`, 32 bytes → base64url; PostgreSQL mints inside
the same statement's parameter set via Go before the atomic UPDATE) and
increments two monotonic counters: `generation` (the fencing epoch) and
`attempts`. Mutating operations — heartbeat, release, complete — require the
full tuple `(tenant, run, owner, token, generation)` to match the stored row
exactly. Mismatch ⇒ typed conflict (`run_claim_stale_fencing`,
`run_claim_lease_expired`, `run_claim_held`); unknown or foreign-tenant rows
⇒ uniform not-found. Nothing is ever a silent success. Read paths redact the
token: it travels only inside acquisition responses.

**Expiry and reclaim.** An expired lease (`expires_at <= store-clock now`)
is claimable again by anyone, including through the bounded batch op.
Reclaim bumps generation and attempts, fencing out the dead holder. The
expired holder cannot revive its epoch by heartbeating: heartbeat requires
an unexpired deadline, so a lapsed lease is forfeited even if unreclaimed.

**Store-owned clock; durations-only ports.** As in ADR-0011, each adapter
owns exactly one injected `ports.Clock`. Public signatures take durations
(`LeaseFor`, `extendFor`) and never caller timestamps; deadlines are computed
with saturating arithmetic (`domain.ClaimExpiry`) against the store clock.

**Atomic creation.** `Create` inserts the initial runnable claim inside the
caller's unit of work (ADR-0010): the claim comes into existence atomically
with `StartRun`'s run insert, and a rolled-back unit leaves no claim — nor
any event/outbox partial state from the same unit. Worker-side wiring into
`StartRun` lands in part 2.

**Bounded dispatch.** `AcquireNext(limit)` claims runnable-or-expired rows in
deterministic `(created_at, run_id)` order. PostgreSQL implements each claim
as an atomic `UPDATE … WHERE (tenant_id, run_id) IN (SELECT … ORDER BY …
LIMIT 1 FOR UPDATE SKIP LOCKED)`: concurrent claimers skip locked rows and
never overlap. Tokens are minted per row in Go, which is why the batch loops
single-row statements instead of one multi-row UPDATE — every claim stays
atomic and adapter-independent. Memory serializes on the shared write lock,
its exact equivalent.

**Terminal cleanup.** `CleanupTerminal` deletes any leftover claim once the
run itself reached a terminal status (`completed | failed | cancelled`). It
refuses non-terminal runs (typed invalid) and is idempotent for missing
rows; terminality is absorbing, so the check-then-delete order is race-free.

**Shared contract.** Both adapters satisfy the identical deterministic suite
`storetest.RunRunClaims` driven by `storetest.AdvancingClock`: UoW creation
atomicity, exclusive concurrent claiming, expiry reclaim counters, stale/
foreign credential rejection per operation, heartbeat window arithmetic,
release history preservation, fenced completion, guarded idempotent cleanup,
rollback leaving no claim/event/outbox row, nested-unit joining, ordering,
and typed validation.

Explicitly deferred to **part 2** (PR D2): worker loops, engine handoff,
heartbeat scheduling policy, configuration surface, server lifecycle.

## Consequences

- Recovery becomes possible without guesswork: any worker can safely adopt an
  expired run; no worker can act on a claim it does not hold.
- Generation checks must wrap any future external side effect tied to a run
  (part 2); the storage layer enforces credentials but cannot fence VMs.
- Attempts is observational (reclaim counts), not a retry budget; caps belong
  to engine policy in part 2.
- One latent bug surfaced by the new shared suite was fixed:
  `RunRepository.Update` on PostgreSQL nulled the NOT NULL `principal`
  column for principals saved as empty string.
