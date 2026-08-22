# Tranche 2 — Transactional outbox: evidence record

Date: 2026-08-22
Branch: `feat/outbox` → PR C (against `main`)
Environment: macOS 26.x, Apple Silicon (arm64), Go 1.25.5/1.26.7 toolchain,
Docker 29.1.3 (disposable postgres:16-alpine container).

Every check below lists the exact command and the observed result. A check
that was not executed is recorded as BLOCKED/NOT RUN, not as PASS.

## Scope

Durable delivery for domain events (ADR-0011):

- `db/migrations/0005_outbox.sql` — outbox table + dispatch index.
- `ports.OutboxStore` — publish / lease / ack / fail-with-backoff / stats.
  All scheduling decisions read the adapter's own injected `ports.Clock`;
  public signatures carry durations only (no `AvailableAt`/`AvailableBefore`
  parameters anywhere).
- Atomic dual write in `EventRepository.Append`: PostgreSQL path inside
  `Store.Do` (joins the caller's unit or creates one); memory path under the
  same write lock, covered by unit snapshot restore. Committed event ⇒ exactly
  one queued delivery; rolled-back unit ⇒ neither row.
- Both adapters (`internal/store/memory/outbox.go`,
  `internal/store/postgres/outbox.go`) satisfy the identical contract suite
  `storetest.RunOutbox`, driven by a shared manual clock
  (`storetest.AdvancingClock`) — zero sleeps for timing behavior.
- In-process dispatcher (`internal/outbox`) with bounded batches, exclusive
  leases, exponential backoff saturating at 24h, terminal dead-letter,
  at-least-once redelivery after lease expiry. Wired in the composition root;
  started/stopped by `ants serve`.
- Typed validated configuration section `outbox.*` (+ `ANTS_OUTBOX_*` env).
- ADR-0011 authored; ADR-0005 updated to point to it.

## Gate results

| # | Check | Command | Result |
| --- | --- | --- | --- |
| 1 | Format | `gofmt -l .` | **PASS** (clean) |
| 2 | Vet | `go vet ./...` | **PASS** |
| 3 | Static analysis | `go run honnef.co/go/tools/cmd/staticcheck@2026.2.1 ./...` | **PASS** |
| 4 | Focused tests | `go test ./internal/outbox/ ./internal/store/storetest/` | **PASS** |
| 5 | Unit + contract tests | `go test ./...` | **PASS** |
| 6 | Race detector | `go test -race ./...` | **PASS** |
| 7 | Migrations + full contract suite vs real PostgreSQL | `./scripts/test-postgres.sh` (race mode, disposable container) | **PASS** — after fixing a real placeholder-numbering bug in `FailWithBackoff` that only real PostgreSQL exposed (`could not determine data type of parameter`) |
| 8 | Full CI gate | `make ci` (fmt-check, vet, staticcheck, tidy-check, manifest-check, test, test-race, build, contracts-test, contracts-drift) | **PASS** |
| 9 | Demo | `make demo` (real git, real exec) | **PASS** — ready_for_review=true, budget within caps |

## Negative paths covered by new tests

- Duplicate publish on dedup key is an idempotent no-op (both adapters).
- Publish rejects `MaxAttempts` outside [1,100] as typed invalid; no row left.
- Concurrent leasers never claim the same message (8 workers × 20 messages);
  every message claimed exactly once under `-race`.
- Ack without lease / double ack / foreign-lessee ack ⇒ uniform not-found.
- Backoff window hides message before retry instant; exhausted attempts
  dead-letter terminally and are never reclaimable.
- Expired lease is reclaimable by another worker; redelivery counts attempts.
- Event append enqueues exactly one delivery whose dedup key and envelope
  carry the stable event ID (both adapters + dedicated PG atomicity test).
- Rolled-back unit leaves neither event nor outbox row (both adapters).
- Dispatcher: scripted failures retry then deliver once; dead-letter after max
  attempts is terminal; crashed worker's lease expires and work redelivers;
  context cancel stops `Run` cleanly; backoff overflow cannot wrap negative.

## Honest limitations of this tranche

1. Delivery sink is the structured log; external subscribers arrive with the
   integrations wave (ADR-0006) behind the same port.
2. The dispatcher is single-process; multi-node scale-out swaps the driver,
   not the port shape.
3. Dead-letter requeue/discard tooling is operator future work; state is
   observable via `Stats`.
4. Outbox rows are retained after delivery; retention/GC policy deferred.
