# Tranche 2 / PR D2 — Run worker integration (ADR-0012 part 2): evidence record

Date: 2026-08-22
Branch: `feat/worker-service` → PR D2 (against `main`)
Base: `main` @ 6c0efe5 ("feat: durable run claims — tenant-scoped fencing
aggregate (ADR-0012 part 1) (#11)")
Environment: macOS 26.x, Apple Silicon (arm64), Go toolchain (auto-switched
to go1.26.7 for staticcheck 2026.2.1), Docker (disposable postgres:16-alpine
container).

Every check below lists the exact command and the observed result. A check
that was not executed is recorded as BLOCKED/NOT RUN, not as PASS.

## Scope

Worker integration per ADR-0012 part 2 — runs execute through durable claims,
never request lifecycles:

- `internal/worker/worker.go` — process-level run executor: bounded rounds of
  `AcquireNext` capped at `min(batch_size, concurrency)` so no claim sits
  queued on an un-renewed lease; per-claim dispatch by run status (pending →
  execute, terminal → guarded leftover cleanup, anything else → interrupted
  convergence); heartbeat renewal loop whose failure cancels execution and
  fences the epoch from any further write; all terminal persistence
  (Complete / Release / recovery / cleanup) on `context.WithoutCancel` +
  bounded `cleanup_timeout`; validated `Config` with production floors.
- `internal/orchestration/recovery.go` — `Engine.RecoverInterrupted`:
  converges an abandoned mid-flight run to `failed` with code
  `run_interrupted` in one unit of work (run update + classified status
  event + automatic outbox enqueue), routes the thread like any failure,
  idempotent for terminal runs, refuses pending runs, touches no claim row.
- Server lifecycle: StartRun only enqueues (`launchRunWorker` and the
  server-side `runWG` deleted); the run worker starts next to the outbox
  dispatcher in `ants serve`, and shuts down in order — HTTP drain → stop
  claiming + wait for executions → final outbox round — under one operator
  budget (`server.shutdown_timeout`) that names the phase that did not
  finish, including whether the budget was exhausted before the final outbox
  round could even be attempted.
- Composition: `app.Build` wires one stable node identity (hostname-pid)
  for both the outbox lease holder and the run-claim owner; `ants serve`
  owns the worker goroutine until shutdown.
- Config surface: `worker:` section (batch_size, interval, lease,
  heartbeat_every, cleanup_timeout, concurrency) with startup validation
  (`lease >= 3x heartbeat_every`; floors: interval >= 10ms, lease >= 1s,
  cleanup_timeout >= 100ms, concurrency in [1,64]), full `ANTS_WORKER_*`
  environment layering, example YAML documentation.
- ADR-0012 updated with the part 2 decision record.

## Gate results

| # | Check | Command | Result |
| --- | --- | --- | --- |
| 1 | Format | `gofmt -l .` (after fixing one long line in `config_test.go`) | **PASS** (no output) |
| 2 | Vet | `go vet ./...` | **PASS** (exit 0) |
| 3 | Static analysis (pinned) | `go run honnef.co/go/tools/cmd/staticcheck@2026.2.1 ./...` | **PASS** (exit 0) |
| 4 | Focused worker suite | `go test ./internal/worker/ -count=1 -v` — 13 tests incl. lost-lease fencing, crash reclaim, once-only interruption convergence, detached-shutdown release, concurrency cap, leftover cleanup, unreadable-run retry | **PASS** |
| 5 | Stress: timing-sensitive suites | `go test ./internal/worker/ -count=10 -race`; `go test ./internal/orchestration/ -count=5 -run TestRecoverInterrupted` | **PASS** (no flakes, race clean) |
| 6 | Focused recovery + server suites | `go test ./internal/orchestration/ -count=1` (incl. 5 new `TestRecoverInterrupted*` tests); `go test ./internal/server/ -count=1` (incl. `TestStartRunOnlyEnqueues` without runtime and full-pipeline e2e with runtime) | **PASS** |
| 7 | All unit + contract tests | `go test ./...` | **PASS** |
| 8 | Race detector, whole repo | `go test -race -count=1 ./...` | **PASS** |
| 9 | Migrations + both contract suites vs real PostgreSQL | `./scripts/test-postgres.sh` (race mode, disposable postgres:16-alpine, fresh DB per binary) | **PASS** — migrate ok; `TestPostgresStoreContract` incl. `RunOutbox` + `RunRunClaims` ok |
| 10 | Full CI gate | `make ci` (fmt-check, vet, lint, tidy-check, manifest-check, test, test-race, build, contracts-test [19 pass], contracts-drift) | **PASS** (exit 0) |
| 11 | Demo vertical slice | `make demo` | **PASS** — five verification criteria exit 0, budget tasks 2/8 exec-ops 5/64 |

## Behaviors pinned by the new tests

Worker (`internal/worker/worker_test.go`, manual advancing clock + channel
hand-offs; wall-clock waits only bound deadlocks, none orders semantics):

- Happy path: claim → execute → run completed → fenced Complete removes the
  claim; no recovery invoked.
- Heartbeats extend the live lease from the current store instant while the
  engine works.
- Lease loss mid-execution cancels the execution (outcome is
  `context.Canceled`), writes no terminal state, cannot revive its lapsed
  lease (typed conflict), leaves the claim exactly as lapsed until reclaim,
  and is fenced out of Release/Complete once a successor epoch exists.
- A crashed holder's expired claim is reclaimed with generation/attempts
  bump and a fresh token; the restarted worker finishes the run.
- An abandoned non-pending run converges exactly once to classified failure;
  a second round finds nothing to recover.
- Shutdown cancellation releases the claim for retry on the detached bounded
  context; the released run stays pending for the next epoch.
- Concurrency cap: a BatchSize larger than Concurrency never claims more
  than there are slots; queued claims stay runnable and are picked up next
  round; max simultaneous executions never exceeds the cap across rounds.
- Leftover claims behind terminal runs (runnable or expired-held) are cleaned
  idempotently and never routed through recovery.
- Unreadable run (persistence outage behind the run store) leaves its claim
  to expire for retry; no execution starts.
- Config validation: floors, margin boundary `lease == 3x heartbeat_every`
  at production-valid magnitudes, rejection of missing dependencies or an
  empty owner identity.

Recovery (`internal/orchestration/recovery_test.go`):

- Convergence commits run→failed(`run_interrupted`) + classified status
  event + thread routing, enqueuing exactly two outbox messages (one per
  event, ADR-0011).
- Injecting an event-append failure inside the unit rolls back atomically:
  run untouched (status/version/failure), thread untouched, zero outbox
  delta.
- Second recovery of an already-terminal run succeeds emitting nothing.
- Pending runs are refused as typed conflict with no side effects.
- Recovery never mutates claim rows: the crashed epoch's credentials survive
  untouched; deletion stays with credential-guarded operations.

Server (`internal/server/`):

- Without the runtime, start-run returns 202 with the run pending and
  exactly one runnable unowned claim — no request-scoped execution exists.
- With the runtime (worker + dispatcher started exactly as `ants serve`
  does), the same API flow completes end-to-end: polling reaches
  `completed` with report, events, and artifacts intact.

Config (`internal/config/config_test.go`): worker bounds reject invalid
values (including a heartbeat without margin inside the lease) and the full
`ANTS_WORKER_*` env layer applies and validates.

## Real defects found and fixed by this tranche's work

1. `internal/worker/worker.go`: `safeErr(nil)` panicked (nil dereference) in
   the post-execution disposal path when the engine returned nil without a
   terminal status. Made nil-safe; the log now reports `<nil>` truthfully.
2. Worker tests assumed `AcquireNext` would follow seeding order, but seeded
   claims shared one `CreatedAt` instant (the manual clock only moves when
   advanced), so batch selection fell through to random-ID tie-breaks.
   Fixed by giving each seeded run its own creation instant — matching the
   production `(created_at, run_id)` dispatch order semantics.
3. The lost-lease test closed its completion gate in a race with heartbeat
   failure detection, so a round could finish unfenced and hit the defect in
   (1). The scripted execution now ends only through cancellation, making
   "round finished means epoch was fenced" a structural property rather than
   a timing bet.
4. `pathTo(RunExecuting)` returned no transitions, so the interrupted-run
   scenario silently left the run pending (the round would hang executing an
   unscripted run). Legal transition chains extended to intermediate
   statuses.
5. Legitimate test-config floor violations from the interrupted prior
   session (heartbeat 2ms vs 10ms floor, lease below the 1s floor, Run-loop
   interval 2ms vs 10ms floor) were corrected to production-valid bounds
   instead of weakening the validators.
6. `ants serve` shutdown could blame the outbox dispatcher for "not draining"
   even when earlier phases had already exhausted the shutdown budget before
   the final outbox round could be attempted. Failure reporting now
   distinguishes the two cases.

## Honest limitations and scope notes of this tranche

1. `make demo` drives `Engine.Execute` directly: it is the pre-existing
   deterministic vertical slice, not the durable claim-driven path. Claim
   driven execution end-to-end is proven by the API runtime e2e suite
   instead.
2. No separate worker-loop-on-PostgreSQL test was added. Judgment: the worker
   loop is adapter-independent code over `ports.RunClaimStore`, and every
   store operation it performs — batch acquire with SKIP LOCKED, heartbeat
   window arithmetic, fenced Complete/Release, guarded idempotent cleanup,
   unit-of-work atomicity — is already pinned against real PostgreSQL by the
   shared contract suite (`storetest.RunRunClaims` inside
   `./scripts/test-postgres.sh`). A PG-specific loop test would re-exercise
   the same SQL predicates through a slower path without pinning new
   behavior.
3. `attempts` remains observational (reclaim counts); retry caps stay engine
   policy for a later tranche.
4. Fencing of external side effects beyond process-internal writes (VMs) is
   enforced here by cancelling fenced executions before disposal; future
   side-effecting call sites must check the epoch the same way.
