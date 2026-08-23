# ADR 0012 — Durable run claims (part 1: storage aggregate; part 2: worker integration)

Status: accepted (parts 1 and 2 complete)
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
caller's unit of work (ADR-0010): the engine's `StartRun` creates it in the
same unit as its run insert, before the thread transition, so every
successful start yields exactly one claim, an idempotent replay never
duplicates one, and a rolled-back unit leaves no claim — nor any event/outbox
partial state from the same unit. Worker-side acquisition of those claims
lands in part 2.

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

Explicitly deferred to **part 2** (PR D2): worker loops,
acquisition-driven execution handoff, heartbeat scheduling policy,
configuration surface, server lifecycle.

## Decision (part 2 — worker integration)

**A process-level executor owns run execution.** `internal/worker.Worker`
claims bounded batches through `RunClaims.AcquireNext` and drives each fenced
claim through the orchestration engine. StartRun (HTTP path) only enqueues —
it never spawns execution tied to a request. The server handler contract is
"return 202 once the run + claim unit is durable"; the API test suite pins
that no runtime means no execution, and that with the runtime present the
same request completes end-to-end.

**Claim only what you execute now.** Each round acquires at most
`min(batch_size, concurrency)` claims: the worker never holds more leases
than it actively executes and heartbeats, so no claim ever sits queued on an
un-renewed lease. Leftover runnable work is picked up by the next poll round.

**Heartbeat policy.** While the engine executes, a renewal loop extends the
lease every `heartbeat_every` on a context detached from cancellation so an
in-flight renewal always delivers its verdict. Losing the lease — expiry or
any fencing conflict — cancels the execution immediately and marks the epoch
fenced: a fenced epoch performs no Complete, Release, or cleanup at all; its
claim converges through expiry-based reclaim. The config validator enforces
`lease >= 3 × heartbeat_every`, so two consecutive missed beats can never
expire a live holder.

**Terminal persistence outlives cancellation.** Every disposition write
(Complete after success, Release for shutdown/pre-terminal exits, recovery
convergence, terminal cleanup) runs on `context.WithoutCancel` + a bounded
`cleanup_timeout`: a shutdown racing execution must never abort the final
write, and no write may hang forever. Guarded store operations make any race
with a successor epoch a typed conflict, not corruption.

**Interrupted runs converge, never resume.** A claimed run found in any
non-pending, non-terminal state was abandoned mid-flight by an earlier epoch.
The engine's `RecoverInterrupted` finishes it as `failed` with code
`run_interrupted` in one unit of work (run update + classified status event +
outbox enqueue), routes the thread like any failure, is idempotent for
already-terminal runs, refuses pending runs (they were never started and must
execute), and deliberately touches no claim row — deletion stays with the
credential-holding epoch via guarded operations.

**Lifecycle order in `ants serve`.** On shutdown: stop accepting HTTP, stop
claiming and wait for active executions to unwind (their detached writes
still land), then stop the outbox dispatcher for one final delivery round of
the events those writes emitted. One operator budget (`server.shutdown_timeout`)
covers all phases; exhausting it names what did not finish — including
distinguishing "the dispatcher failed its final round" from "the budget was
gone before that round could be attempted". Nothing is silently lost:
whatever remains converges through lease expiry and recovery.

**Configuration surface.** The `worker:` config section (batch_size,
interval, lease, heartbeat_every, cleanup_timeout, concurrency) validates at
startup with production floors (interval ≥ 10ms, lease ≥ 1s, cleanup_timeout
≥ 100ms, concurrency ≤ 64) plus the heartbeat-margin rule; every field is
environment-overridable via `ANTS_WORKER_*`.

## Consequences

- Recovery becomes possible without guesswork: any worker can safely adopt an
  expired run; no worker can act on a claim it does not hold.
- Generation checks wrap every external side effect tied to a run: the worker
  cancels fenced executions before disposal and relies on credential-guarded
  writes everywhere else; the storage layer enforces credentials but cannot
  fence VMs, so future side-effecting call sites must check the epoch the way
  `executeClaim` does.
- Attempts is observational (reclaim counts), not a retry budget; the
  dispatch cap built on top of it landed with ADR-0013
  (`worker.max_attempts` → exhausted-run convergence).
- One latent bug surfaced by the new shared suite was fixed:
  `RunRepository.Update` on PostgreSQL nulled the NOT NULL `principal`
  column for principals saved as empty string.

Update (2026-08-23, PR 3.8): the durable run record gained a read-only,
tenant-scoped listing per thread (`RunStore.ListByThread`, served at
`GET /v1/threads/{id}/runs` in stable `(created_at, id asc)` order). This
touches neither the claims machinery nor the run state machine — terminality
still lives in the closed status table above, and the web console now uses
the listing to discover a thread's live/latest run (ADR-0020).
