# Tranche 2 / PR F — Final audit and closure: evidence record

Date: 2026-08-22
Branch: `audit/tranche-2-final` → PR F (against `main`)
Base: `main` @ 9e38f52 ("feat: operational hardening — bounded dispatch,
honest readiness, production auth gate (ADR-0013) (#13)")
Environment: macOS 26.x, Apple Silicon (arm64), Go 1.25.5 toolchain
(staticcheck 2026.2.1 auto-switched to go1.26.7), Docker 29.1.3 (disposable
postgres:16-alpine container).

Scope: audit/closure only. No Tranche 3 work (OIDC, metrics platform,
dead-letter tooling, retention/GC, multi-node scale-out, new integrations)
was started; none of the already-promised Tranche 2 acceptance criteria were
found missing beyond the two gaps fixed below.

## Audit outcome list (bounded)

1. **GAP — FIXED:** tenant creation was not atomic with its creation event
   (`internal/server/handlers.go`): `Tenants.Create` committed first and
   `emitTenantEvent` ran as a second, best-effort write that only logged
   failures. On PostgreSQL that is two commits; a crash or append failure in
   between leaves a tenant with no event and no durable outbox delivery.
   This contradicted ADR-0010's decision statement ("API handlers wrap
   resource creation with its creation event") and was the only API write
   emitting an event outside a unit of work.
2. **GAP — FIXED:** `handleGetRun` discarded the task-listing error
   (`tasks, _ := s.repos.Tasks.ListByRun(...)`), answering 200 with an empty
   task list during a store outage — a silent fallback violating AGENTS.md
   and MASTER_PLAN §15.1 ("no silent fallbacks", no ignored errors).
3. **DOC DRIFT — FIXED:** SECURITY.md claimed "No durable outbox yet
   (ADR-0005)" and "single-process until the Postgres adapter lands" — false
   since PR #7 (adapters) and PR #10 (transactional outbox). Rewritten to
   state the real current posture and deferrals.
4. **DOC DRIFT — FIXED:** README.md still described tranche 1 as the current
   implementation state and pointed to TRANCHE_1_EVIDENCE.md. Status updated
   to cover Tranche 2; evidence pointer moved to this record.
5. **AUDITED CLEAN:** tenant scoping/auth posture — every authenticated
   handler resolves a principal from context before touching stores; all
   store calls take explicit tenant IDs; cross-tenant reads are uniform 404
   (pinned by `TestCrossTenantIsolation`, `storetest.RunRunClaims`
   foreign-tenant subtests); dev-header auth is refused on non-loopback binds
   at startup (ADR-0013) and re-proven live below.
6. **AUDITED CLEAN:** UoW state+event+outbox atomicity on every engine path —
   run/thread/task transitions, StartRun (+ claim create), recovery, exhausted
   convergence all wrap state + status event in one unit; the event append
   enqueues exactly one delivery inside the same unit. Pinned by
   `storetest.RunOutbox` dual-write/rollback subtests and
   `startrun_claims_test.go` failure injection, against both adapters.
7. **AUDITED CLEAN:** PostgreSQL/memory contract parity — the identical
   deterministic suites (`storetest.Run`, `RunOutbox`, `RunRunClaims`) run
   against memory in `go test ./...` and against disposable PostgreSQL via
   `scripts/test-postgres.sh`; both green below.
8. **AUDITED CLEAN:** run-claim lifecycle — creation inside the StartRun unit,
   exclusive acquire/acquire-next (SKIP LOCKED / write-lock serialization),
   heartbeat window arithmetic with the lease ≥ 3× heartbeat margin enforced
   at config validation, fencing credentials on every mutation, expiry reclaim
   bumping generation/attempts, terminal cleanup idempotency, and max-attempts
   dispatch convergence (boundary: acquisition == budget still executes).
   Pinned by `storetest.RunRunClaims` (14 subtests), `worker_test.go`,
   `exhausted_test.go`, `recovery_test.go`.
9. **AUDITED CLEAN:** shutdown ordering and readiness — `ants serve` drains
   HTTP → stops claiming/waits for executions → final outbox round under one
   budget, naming the phase that failed including "budget gone before the
   final round"; readiness is an injected bounded check (503 typed transient
   on failure, liveness independent). Pinned by server tests and re-proven
   live below.
10. **PROBED, ACCEPTED (no churn):** request-ID echo — an incoming
    `X-Request-ID` containing CRLF cannot inject response headers because the
    HTTP request parser splits header values on CRLF before Go code sees them
    (verified empirically against a raw listener: the injected header never
    appears on the wire). Unbounded reflection length remains a cosmetic nit;
    recorded here rather than churned.

## Traceability matrix — canonical plan → PRs → proof

| Plan requirement (MASTER_PLAN) | Delivered by | Code | Tests/gates |
| --- | --- | --- | --- |
| PG source of truth behind ports (§14.6, ADR-0009) | PR #7 | `internal/store/postgres/*`, `db/migrations/*` | `storetest.Run` vs both adapters; `scripts/test-postgres.sh` |
| Transition+event one commit (§6.3, ADR-0010) | PR #8 | `ports.Transactor`, `store/memory/transactor.go`, engine transition helpers | rollback-on-error-and-panic + nested-unit subtests; `TestStartRunRollsBack*` |
| Deterministic cancel semantics | PR #9 | `internal/orchestration` cancellation paths | deterministic cancel test (`engine_test.go`) |
| Outbox in same commit; at-least-once (§6.3, ADR-0011) | PR #10 | `db/migrations/0005_outbox.sql`, `ports.OutboxStore`, `internal/outbox` | `storetest.RunOutbox`; dispatcher tests; config `outbox.*` |
| Durable run claims: fencing/reclaim (ADR-0012 p1) | PR #11 | `domain/runclaim.go`, `ports/runclaims.go`, `db/migrations/0006_run_claims.sql`, both adapters | `storetest.RunRunClaims`; `startrun_claims_test.go` |
| Worker owns execution; recovery; shutdown order (ADR-0012 p2) | PR #12 | `internal/worker`, `orchestration/recovery.go`, `cmd/ants serve` lifecycle, `worker:` config | `worker_test.go` (13 tests); `TestStartRunOnlyEnqueues`; e2e runtime test |
| Bounded dispatch, honest readiness, auth gate, listener bounds (ADR-0013) | PR #13 | `orchestration/exhausted.go`, injected `Ready`, loopback validation, `idle_timeout`, `MaxBytesReader` fix | `exhausted_test.go`; readyz tests; config tests; live smokes below |
| Audit closure of the above (this PR F) | PR F | Uow seam into server; tenant unit fix; error propagation | new atomicity tests; full gates below |

## Gate results

| # | Check | Command | Result |
| --- | --- | --- | --- |
| 1 | Format | `gofmt -l .` (after formatting the new Deps field block) | **PASS** (no output) |
| 2 | Vet | `go vet ./...` | **PASS** |
| 3 | Static analysis (pinned) | `go run honnef.co/go/tools/cmd/staticcheck@2026.2.1 ./...` | **PASS** (exit 0) |
| 4 | New focused tests | `go test ./internal/server/ -run "TestTenantCreationIsAtomicWithItsEvent\|TestGetRunSurfacesTaskListFailure" -count=1 -v` | **PASS** (2/2) |
| 5 | All unit + contract tests | `go test -count=1 ./...` | **PASS** (15 packages) |
| 6 | Race detector, whole repo | `go test -race -count=1 ./...` | **PASS** |
| 7 | Stress: timing-sensitive suites | `go test ./internal/worker/ -count=10 -race`; `go test ./internal/orchestration/ -count=5 -race -run "RecoverInterrupted|ConvergeExhausted|Exhausted|Interrupted"` | **PASS** (no flakes, race clean) |
| 8 | Migrations + both contract suites vs real PostgreSQL | `./scripts/test-postgres.sh` (disposable postgres:16-alpine, fresh DB per binary) | **PASS** — migrate ok; contracts incl. RunOutbox + RunRunClaims ok |
| 9 | Full CI gate | `make ci` (fmt-check, vet, lint, tidy-check, manifest-check, test, test-race, build, contracts-test [19 pass], contracts-drift) | **PASS** (exit 0) |
| 10 | Demo vertical slice | `make demo` (real git, real exec) | **PASS** — verification criteria exit 0, budget tasks 2/8 exec-ops 5/64 |
| 11 | TypeScript contracts | `pnpm --filter @ants/contracts test`; `git diff --exit-code -- packages/contracts/src/schema.d.ts` after regenerate | **PASS** (19 pass; drift clean) |
| 12 | Live serve smoke | `./bin/ants serve --config config/ants.example.yaml`; curl `/healthz`, `/readyz`; SIGTERM | **PASS** — healthz 200, readyz 200 `{"status":"ready"}`, shutdown exit 0 |
| 13 | Live tenant-path smoke | POST `/v1/tenants` against the running server | **PASS** — 201 with tenant JSON; SIGTERM shutdown exit 0 |
| 14 | Live auth-gate smoke | example config rewritten to `0.0.0.0:8099` → `./bin/ants config validate --config <that file>` | **PASS** — exit 1, typed refusal naming `dev_header_auth` |

## Behaviors pinned by the new tests

`internal/server/atomicity_test.go` (failure-scriptable store proxies at the
composition boundary; production wiring untouched):

- A failing event append during tenant creation surfaces as its typed
  transient problem (503), leaves **no tenant row**, and enqueues **no outbox
  delivery**; once the outage clears the same request succeeds and commits
  exactly one tenant-created event (unit-of-work atomicity at the API seam,
  ADR-0010/0011).
- A task-store outage during a run read is a typed transient problem (503),
  never a 200 with an empty task list; recovery serves the run again.

## Real defects found and fixed by this audit

1. `internal/server/handlers.go`: tenant creation committed its row and its
   event in two independent writes with the event failure swallowed by a log
   line. Now one unit via the injected transactor; the event-append error is
   surfaced as a typed problem instead of being logged away.
2. `internal/server/handlers.go`: `handleGetRun` ignored the
   `Tasks.ListByRun` error and reported success with an empty list. The error
   now propagates through the standard problem mapping.
3. Supporting change: `server.Deps` requires a `Uow ports.Transactor`
   (nil is a construction error, matching the readiness-seam pattern from
   ADR-0013); `app.App` exposes the composition root's transactor so `ants
   serve` and the API test harness wire it explicitly.

## Documentation truthfulness corrections

- `SECURITY.md`: replaced the stale "no durable outbox yet" limitation with
  the actual posture — transactional outbox shipped (ADR-0011), dev auth
  confined to loopback (ADR-0013), dispatcher scale-out/dead-letter
  tooling/retention explicitly deferred.
- `README.md`: status paragraph now describes both tranches; the "current
  implementation state" pointer moved from TRANCHE_1_EVIDENCE.md to this
  record.

## BLOCKED / NOT RUN

| Item | Status | Reason |
| --- | --- | --- |
| Firecracker/KVM conformance, vfkit/macOS driver | NOT RUN | Requires Linux KVM host / vfkit install (Horizon 2; MASTER_PLAN §16.3) |
| OIDC/Keycloak login | NOT RUN | Horizon 1 item 2; production posture today is "refuse unless dev auth on loopback" (ADR-0004/0013) |
| Live 503 against an unreachable PostgreSQL | NOT RUN | By design: postgres mode fails at startup wiring before any probe exists; the 503 path is pinned at API level (unchanged from PR E evidence) |
| Multi-node dispatcher/worker scale-out | NOT RUN | Explicitly deferred (ADR-0011/0013); single-process contract proven |
| GitHub Actions run for this PR | NOT RUN AT AUDIT TIME | CI triggers on PR creation; local `make ci` is the authoritative gate for this session |

## Honest limitations carried forward (not introduced by this PR)

1. The process sandbox is not a security boundary (ADR-0003); microVM
   drivers remain Horizon 2.
2. Outbox delivery sink is the structured log until the integrations wave;
   consumers deduplicate on event ID.
3. Every run-claim acquisition counts toward `worker.max_attempts`,
   including shutdown-released epochs (ADR-0013); operators must leave
   headroom for deploy churn.
4. Dead-letter requeue/discard tooling and outbox retention/GC remain
   operator-wave work (ADR-0011/0013).

## Deferred to Tranche 3 (explicitly not started here)

OIDC/Keycloak authentication replacing dev headers; Prometheus/OpenTelemetry
metrics exposure; dead-letter operator tooling and outbox retention policy;
multi-node dispatcher/worker scale-out; integration wave A (GitHub fake,
webhooks, MCP); web/PWA surface; Temporal-backed durable engine seam
(ADR-0002); microVM sandbox drivers (Horizon 2).

## Verdict

Tranche 2 is complete and audited: every promised acceptance criterion is
implemented and pinned by deterministic tests on both persistence adapters,
all local gates pass, and the remaining gaps are documented deferrals rather
than hidden ones. Release/readiness verdict for the tranche: **PASS** — with
the standing project caveat that this tree is still not production-ready for
untrusted workloads (no VM isolation, no OIDC yet).
