# Tranche 2 / PR E — Operational hardening (ADR-0013): evidence record

Date: 2026-08-22
Branch: `feat/operational-hardening` → PR E (against `main`)
Base: `main` @ 82d6c99 ("feat: run worker integration — durable claim
execution (ADR-0012 part 2) (#12)")
Environment: macOS 26.x, Apple Silicon (arm64), Go toolchain (auto-switched
to go1.26.7 for staticcheck 2026.2.1), Docker (disposable postgres:16-alpine
container).

Every check below lists the exact command and the observed result. A check
that was not executed is recorded as BLOCKED/NOT RUN, not as PASS.

## Scope

Final production-readiness PR of tranche 2, closing the four operational
gaps left explicitly deferred by prior tranches (ADR-0013):

- Bounded dispatch per claim: `worker.max_attempts` (default 3, validated
  [1,10], env `ANTS_WORKER_MAX_ATTEMPTS`). An acquisition beyond the budget
  converges its run instead of executing — cancelled if still pending (the
  run machine has no pending→failed edge; cancellation is the honest state
  for work that never started), failed with code `run_attempts_exhausted` if
  abandoned mid-flight. Both paths commit run + classified status event in
  one unit of work, route the thread like their outcome does, are idempotent
  for terminal runs, and touch no claim row. This closes D2's deferred
  "retry caps stay engine policy" item and mirrors outbox max-attempts
  semantics (`internal/orchestration/exhausted.go`, worker dispatch).
- Honest readiness: the sentinel-slug probe (`__readiness_probe__`) is
  deleted. The server now requires an injected readiness function (nil is a
  construction error, never an implicit always-ready); the composition root
  supplies PostgreSQL pool ping per store mode and a trivially satisfied
  check for memory. Each probe is bounded by new `server.readiness_timeout`
  (default 2s, env `ANTS_SERVER_READINESS_TIMEOUT`); failure is a transient
  RFC 9457 problem (503 `store_unavailable`); liveness stays independent.
- Production auth gate (ADR-0004 enforcement): startup validation refuses
  `dev_header_auth: true` on any non-loopback bind (literal loopback IPs or
  `localhost` only) with a typed error naming the fix.
- HTTP listener bounds: new `server.idle_timeout` (default 120s, env
  `ANTS_SERVER_IDLE_TIMEOUT`, validated positive) reclaims idle keep-alive
  connections; `MaxHeaderBytes` pinned to the stdlib default explicitly;
  strict JSON decoding passes the real ResponseWriter into
  `http.MaxBytesReader` so oversized bodies flag the connection for teardown.
- Config/docs registry sync: example YAML gains the previously missing
  documented `outbox:` section plus all new fields; ADR-0013 authored,
  ADR-0004 and ADR-0012 updated.

## Gate results

| # | Check | Command | Result |
| --- | --- | --- | --- |
| 1 | Format | `gofmt -l .` | **PASS** (no output) |
| 2 | Vet | `go vet ./...` | **PASS** |
| 3 | Static analysis (pinned) | `go run honnef.co/go/tools/cmd/staticcheck@2026.2.1 ./...` | **PASS** (exit 0) |
| 4 | Focused suites | `go test ./internal/orchestration/ ./internal/worker/ ./internal/server/ ./internal/config/ -count=1` | **PASS** |
| 5 | Stress on timing-sensitive suites | `go test ./internal/worker/ -count=10 -race`; `go test ./internal/orchestration/ -count=5 -run "ConvergeExhausted\|RecoverInterrupted"` | **PASS** (no flakes, race clean) |
| 6 | All unit + contract tests | `go test ./...` | **PASS** |
| 7 | Race detector, whole repo | `go test -race -count=1 ./...` | **PASS** |
| 8 | Migrations + both contract suites vs real PostgreSQL | `./scripts/test-postgres.sh` (disposable postgres:16-alpine, fresh DB per binary) | **PASS** — migrate ok; store contracts incl. RunOutbox + RunRunClaims ok |
| 9 | Full CI gate (run twice) | `make ci` (fmt-check, vet, lint, tidy-check, manifest-check, test, test-race, build, contracts-test [19 pass], contracts-drift) | **PASS** (exit 0 both runs) |
| 10 | Demo vertical slice | `make demo` | **PASS** — ready_for_review=true, budget tasks 2/8 exec-ops 5/64 |
| 11 | Live serve smoke | `./bin/ants serve --config config/ants.example.yaml`; curl healthz/readyz; SIGTERM | **PASS** — healthz 200, readyz 200 `{"status":"ready"}`, shutdown exit 0 |
| 12 | Live auth-gate smoke | dev-auth config rewritten to `0.0.0.0:8080` → `ants config validate --config /tmp/ants-exposed.yaml` | **PASS** — exit 1, typed refusal naming `dev_header_auth` |

## Behaviors pinned by the new tests

Worker (`internal/worker/worker_test.go`, manual clock + channel hand-offs;
wall-clock waits only bound deadlocks):

- Boundary: an acquisition landing exactly on `max_attempts` still executes
  to completion and invokes no convergence (generation asserted == budget).
- Over-budget pending run converges to `cancelled` without ever executing
  (executor start channel + outcomes prove no dispatch), claim deleted, and
  a second round finds nothing to converge — exactly-once.
- Over-budget mid-flight run converges exhausted, never as interrupted
  (recovery recorder stays empty).
- Injected convergence failure keeps the held claim until expiry and leaves
  the run untouched (no half-converged state); after the outage clears and
  the lease lapses, the reclaimed epoch converges and cleans up.

Engine (`internal/orchestration/exhausted_test.go`):

- Mid-flight convergence commits failed(`run_attempts_exhausted`,
  transient=false) + status event carrying code and attempt count, enqueues
  exactly two outbox messages, routes thread → failed.
- Pending convergence cancels the run (no failure info), carries the
  exhausted classification on the cancellation event, routes planning thread
  → awaiting_input like cancellation does.
- Repeat convergence of a terminal run emits nothing (idempotent).
- Injected event-append failure rolls the unit back atomically: run,
  version, failure, thread, outbox all untouched.
- Convergence never mutates a crashed epoch's claim credentials; deletion
  stays with credential-guarded operations.

Server (`internal/server/`):

- Failing dependency check ⇒ 503 typed problem `store_unavailable`; healthz
  stays 200 in the same state; recovery flips readyz back to 200 without a
  restart (flippable injected check).

Config (`internal/config/config_test.go`):

- Dev-header auth validates on every loopback form (127.x, ::1, localhost)
  and fails on wildcard/LAN/hostname binds with a message naming
  `dev_header_auth`; identical binds validate fine with dev auth off.
- New timeouts and `max_attempts` layer via their env vars and validate;
  zero/negative timeouts and out-of-range attempts rejected.

## Real defects found and fixed by this tranche's work

1. `/readyz` probed persistence by reading a sentinel tenant slug — a fake
   signal that could not distinguish pool health from query success and hid
   the dependency seam inside the HTTP package. Replaced by the injected,
   bounded readiness seam.
2. `http.Server` had no `IdleTimeout`: keep-alive sockets were held open
   indefinitely against the write-timeout assumption, an easy resource leak
   under idle connection churn.
3. `decodeStrict` passed a nil `http.ResponseWriter` into
   `http.MaxBytesReader`, silently skipping the connection-teardown flag for
   oversized bodies.
4. `docs/TRANCHE_2_EVIDENCE.md`'s example configuration never documented the
   `outbox:` section that PR C introduced — the example YAML lagged the env
   registry. Synced.

## Honest limitations and scope notes of this tranche

1. No live 503 demonstration against a real unreachable PostgreSQL: in
   postgres mode a dead database fails at startup wiring (connect ping),
   before any probe exists. The 503 path is pinned deterministically at the
   API level instead (TestReadyzFailsWhenDependencyUnavailable). NOT RUN as
   a live scenario by design of the current startup semantics.
2. Metrics exposure (Prometheus/OpenTelemetry), dead-letter requeue/discard
   tooling, outbox retention/GC, and multi-node dispatcher scale-out remain
   deferred to their respective waves, exactly as recorded in ADR-0013 and
   the PR C/D evidence records. Nothing in this PR pretends otherwise.
3. Every acquisition consumes budget, including shutdown-released epochs;
   documented in the config surface and ADR-0013 rather than hidden behind a
   second counter. Operators sizing `max_attempts` must account for deploy
   churn.
4. `make demo` still drives `Engine.Execute` directly (pre-existing
   deterministic slice); the durable claim-driven path remains proven by the
   API runtime e2e suite, unchanged from D2.
