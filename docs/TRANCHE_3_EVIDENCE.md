# Tranche 3 / PR D1 — Durable run claims (storage aggregate): evidence record

Date: 2026-08-22
Branch: `feat/run-claims` → PR D1 (against `main`)
Base: `main` @ b4a390c
Environment: macOS 26.x, Apple Silicon (arm64), Go 1.25.5 toolchain,
Docker 29.1.3 (disposable postgres:16-alpine container).

Every check below lists the exact command and the observed result. A check
that was not executed is recorded as BLOCKED/NOT RUN, not as PASS.

## Scope

Storage-level run-claim aggregate per ADR-0012 part 1:

- `internal/domain/runclaim.go` — `RunClaim` aggregate, closed
  `runnable ⇄ claimed` transition table (wired into the shared consistency
  test), crypto-random opaque token minting (`crypto/rand`, 32 bytes →
  base64url), owner/fencing validation, saturating lease arithmetic.
- `internal/ports/runclaims.go` — versioned contract
  (`ants.store.runclaims.v1`): create/get/acquire/acquire-next/heartbeat/
  release/complete/cleanup-terminal. Durations only — no public signature
  accepts a caller timestamp; all scheduling instants come from the adapter's
  injected clock.
- `db/migrations/0006_run_claims.sql` — forward-only migration: composite PK
  `(tenant_id, run_id)`, `UNIQUE (run_id)`, FKs to tenants/runs, CHECKs
  pinning status ∈ {runnable, claimed}, claimed ⇔ owner/token/expiry present,
  generation ≥ 1 and attempts ≥ 1 while claimed; dispatch index
  `(status, expires_at)`. Applied by the embedded migrator in every
  integration run.
- Memory adapter with full unit-of-work support (snapshot backup/restore
  covers claims) and the injected clock.
- PostgreSQL adapter: atomic conditional UPDATE for single-run acquire;
  bounded batch acquire via `UPDATE … WHERE … IN (SELECT … ORDER BY
  created_at, run_id LIMIT 1 FOR UPDATE SKIP LOCKED)` per row so concurrent
  claimers never overlap; fencing enforced in statement predicates with typed
  conflict/not-found classification; nested units join the caller's
  transaction.
- Shared deterministic suite `storetest.RunRunClaims` (14 subtests) run
  identically against memory and disposable real PostgreSQL on an advancing
  manual clock — zero sleeps.
- ADR-0012 (part 1). Worker goroutines, engine handoff, server lifecycle and
  worker config are explicitly deferred to PR D2.

## Gate results

| # | Check | Command | Result |
| --- | --- | --- | --- |
| 1 | Format | `gofmt -l .` | **PASS** (no output) |
| 2 | Vet + static analysis | inside `make ci` (`go vet ./...`, staticcheck 2026.2.1) | **PASS** |
| 3 | Full CI gate | `make ci` (fmt-check, vet, lint, tidy-check, manifest-check, test, test-race, build, contracts-test, contracts-drift) | **PASS** (exit 0) |
| 4 | Focused claim tests (race) | `go test -race -count=1 -run 'TestMemoryRunClaimContract\|TestRunClaim\|…' ./internal/store/storetest/ ./internal/domain/` | **PASS** |
| 5 | All unit + contract tests | `go test ./...` | **PASS** |
| 6 | Race detector, whole repo | `go test -race ./...` (inside make ci) | **PASS** |
| 7 | Migrations + both contract suites vs real PostgreSQL | `./scripts/test-postgres.sh` (race mode, disposable container, fresh DB per test binary) | **PASS** — migrate 0001→0006 ok; `TestPostgresStoreContract` incl. `RunOutbox` + `RunRunClaims` ok |
| 8 | Demo vertical slice | `make demo` | **PASS** — ready_for_review=true, budget tasks 2/8 exec-ops 5/64 |

## Behaviors pinned by the new suite (both adapters)

- Claim creation is atomic with its run inside a unit of work; a rolled-back
  unit leaves neither row — nor any event/outbox partial state from the same
  unit (dual-write rollback).
- Duplicate create conflicts; unknown-run or cross-tenant create is typed
  invalid.
- First acquisition sets generation=attempts=1 and mints an unguessable
  token; read paths redact the token; a live lease conflicts for any second
  acquirer, including the current holder.
- 8 concurrent workers × `AcquireNext` over 16 runs: every run claimed
  exactly once, attempts==generation==1 everywhere (race detector on).
- Expired leases are reclaimable via single or batch acquire; reclaim bumps
  generation and attempts and mints a fresh token; superseded credentials are
  fenced out of heartbeat/release/complete with typed conflicts.
- Wrong token / wrong generation ⇒ `run_claim_stale_fencing`; heartbeat on a
  lapsed lease ⇒ `run_claim_lease_expired`; unknown or foreign-tenant rows ⇒
  uniform not-found across every operation.
- Heartbeat extends only live leases; window arithmetic asserted around the
  extension boundary (renew near expiry succeeds, one tick past fails).
- Release returns the claim to runnable clearing ownership while preserving
  generation/attempts history; the next acquisition continues the monotonic
  counters.
- Complete requires exact fencing, removes the claim, and repeating it is
  typed not-found — never silent success.
- Terminal cleanup refuses non-terminal runs (invalid), deletes once the run
  is cancelled/completed/failed, and is idempotent; foreign-tenant and
  unknown runs are not-found.
- Nested units join the outer unit: an inner run+claim insert rolls back with
  the outer failure.
- Batch claiming respects the limit and deterministic creation order.

## Real bugs found by this tranche's tests

- `internal/store/postgres/runs.go`: `Update` wrote
  `principal = NULLIF($7,'')::text`, turning an empty principal into NULL and
  violating `runs.principal NOT NULL` — first exercised on PostgreSQL by the
  new terminal-cleanup contract step. Fixed to write the value verbatim.

## Honest limitations of this tranche

1. Nothing acquires claims yet in production paths: worker loops, engine
   handoff, heartbeat policy, config surface and lifecycle wiring are PR D2
   (ADR-0012 part 2).
2. `attempts` is observational (counts acquisitions); retry caps belong to
   engine policy in part 2.
3. The storage layer fences store operations; fencing of external side
   effects (VM execution) must be applied at the call sites introduced in
   part 2.
