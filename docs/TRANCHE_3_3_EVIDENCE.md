# Tranche 3 / PR 3.3 — Outbox retention/GC: bounded deletion of terminal rows — evidence record

Date: 2026-08-23
Branch: `feat/outbox-retention-gc` → PR against `main`
Base: `main` @ 2835618 ("feat: outbox dead-letter operator tooling (#16)")
Environment: macOS arm64, Go toolchain 1.25.x (staticcheck 2026.2.1
auto-switches to go1.26.7), Docker postgres:16-alpine (disposable),
pnpm/openapi-typescript 7.13.0.

Scope: one Tranche 3 outcome only — retention/GC for terminal outbox rows
per ADR-0016. Config horizons/bounds, port model, memory and PostgreSQL
adapters through one shared contract suite, migration 0008, GC service seam
with optional scheduled loop, metrics amendment, CLI preview/sweep with an
explicit confirmation gate, runbook/README/SECURITY posture. No dead-letter
collection, no per-tenant retention policy, no remote APIs, no OpenAPI
changes.

## Requirement → code → test matrix

| # | Requirement | Code | Tests |
|---|---|---|---|
| R1 | Retention semantics decided against plan + ADRs and recorded; inert unless intentionally configured; distinct delivered/discarded horizons justified; pending/leased/dead/events/audit permanently out of reach | `docs/adr/0016-outbox-retention-gc.md`; zero-horizon exemption enforced in the port itself (`ports.RetentionSweepRequest` docs + adapter guards) | `TestDefaultsValidate` pins inert defaults; storetest `ZeroHorizonExemptsItsClassEntirely`; `SweepDeletesOnlyEligibleTerminalRowsBeyondHorizon`; PG `TestRetentionNeverDeletesRowsWithoutTerminalTimestamp` |
| R2 | Explicit nonzero horizons + batch limits through the existing config system with validation/defaults/env overrides | `config.OutboxRetention` (+Validate/Active), env vars `ANTS_OUTBOX_RETENTION_*`, example YAML section | config tests incl. negative horizon / bad batch / bad interval cases in `TestValidationFailures`; unknown-env guard covers new vars automatically |
| R3 | Domain/ports model: bounded request/result, store-owned cutoff, durations-only requests, DryRun sharing the sweep's selection logic, typed invalid failures; no raw SQL/envelopes across the port | `internal/ports/outboxretention.go`; `OutboxStore.SweepRetention` | storetest `InvalidRequestsAreRejected`, `ResultCutoffIsTheStoreClockInstant`; CLI `TestSweepJSONOutputIsTyped` (result JSON shape) |
| R4 | Parity in memory + PostgreSQL via one shared behavioral suite: deterministic oldest-first bounded deletion, only eligible terminal rows beyond horizon, strict inclusive boundary, concurrency safety, idempotent reruns, UoW rollback parity, global-round scoping | memory `SweepRetention` (`internal/store/memory/outbox.go`); PG `SweepRetention` + victim DELETE/SELECT helpers (`internal/store/postgres/outbox.go`) | `storetest.RunOutboxRetention` (10 subtests) wired into both `TestMemoryOutboxRetentionContract` and `TestPostgresStoreContract`; within-class order pinned per adapter by `TestRetentionOldestFirstVictimOrder` (PG) and class-priority subtest (shared) |
| R5 | Single bounded atomic deletion strategy with index support in forward-only migration 0008 | migration `db/migrations/0008_outbox_retention.sql` (two partial indexes); PG round wrapped in one unit of work, victims via `FOR UPDATE SKIP LOCKED … LIMIT`, truthful `RETURNING` counts | `TestRetentionIndexesExist`; migrate integration suite applies 0001–0008 idempotently; full contract suite against real PG16 |
| R6 | Application/service seam performing one GC round without violating dispatcher/operator invariants; scheduling decision recorded | `internal/outboxgc/service.go` (`Preview/Round/Run/Active`); serve lifecycle starts the loop only when a horizon is configured and stops it FIRST during shutdown (`internal/cli/cli.go`); rationale in ADR-0016 "Scheduling" | outboxgc tests: construction validation, preview purity, bounded round + observer truthfulness, nil observer, inactive refusal, cancel-stop; live smoke: scheduled loop collects rows, SIGTERM shutdown exits 0 |
| R7 | Metrics only as a reviewed amendment to ADR-0014's closed set; fixed vocabularies; no identifiers as labels; metrics cannot alter behavior | `metrics.Metrics.Deleted/RoundsCompleted` implementing `outboxgc.Observer` structurally; ADR-0014 amendment paragraph | `wantNames` pin + label assertions in `metrics_test.go`; outboxgc `TestNilObserverKeepsBehaviorIdentical`; previews never fire observers (`TestPreviewReportsWithoutDeletingOrObserving`) |
| R8 | CLI/operator UX: dry-run preview, unambiguous --yes gate with proof nothing is deleted when omitted, stable output/JSON, typed error triples, exit codes 0/1/2, config path support | `runOutboxRetention/runRetentionSweep/retentionSweep` (`internal/cli/outbox.go`); refusal prints real preview numbers computed non-destructively | cli tests: `TestUnconfirmedSweepRefusesWithUsageExit`, `TestConfirmedSweepDeletesEligibleTerminalRowsOnly`, `TestRetentionPreviewReportsWithoutDeleting`, `TestRetentionIsInertByDefault`, `TestSweepJSONOutputIsTyped`; live smoke re-proves the gate end-to-end |
| R9 | Runbook, README/SECURITY posture, evidence with honest status, rollback/recovery implications, latest-evidence pointer | `docs/runbooks/outbox-operations.md` retention section; README status + pointer swap to this file; SECURITY.md structural guarantees | human-reviewed; commands below are copy-paste reproducible |

## Design decisions worth knowing (full rationale in ADR-0016)

- **Inclusive boundary:** a row becomes collectable once its terminal age has
  *reached* the horizon (`terminal_at <= cutoff - horizon`). Pinned at exact
  equality by the shared boundary subtest.
- **Zero horizon exempts its class at the port level**, not just in config:
  even a buggy caller cannot widen GC into "everything now".
- **Class priority under tight budget:** delivered victims claim the budget
  first; discarded get the remainder. Deterministic and pinned by contract.
- **Global rounds:** symmetric with the dispatcher's global `Lease`; results
  carry counts only, so there is no enumeration surface. Per-tenant policy
  stays deferred to the managed-cloud era.
- **Serve stops the GC loop FIRST** during graceful shutdown: destructive
  maintenance never competes with worker/dispatcher drains, and skipping one
  atomic round is always safe.

## Audit/deslop findings this session

- **D1 — memory sweep refactored after first pass (deslop):** initial
  implementation aliased `delivered[:take]` and scanned exempt classes;
  rewritten so budget allocation mirrors the PostgreSQL statement structure
  exactly (delivered-first, remainder to discarded), making the two adapters
  visibly symmetric. Tests re-run green.
- **D2 — test determinism vs macOS clock granularity (fixed before landing):**
  service/CLI tests initially used nanosecond horizons against the system
  clock; consecutive `time.Now()` wall readings on darwin quantize to ~1µs,
  making eligibility flaky. Replaced with an injected manual clock
  (outboxgc tests) and short real horizons with a bounded 250ms wait where a
  system-clocked fixture is inherent; no test sleeps for behavior that a
  clock seam can express.
- **D3 — CLI execution core split from flag parsing (matches 3.2's F4
  lesson):** `retentionSweep(app, delete, json, ...)` takes the application
  directly so fixtures exercise the real deletion path instead of building a
  second world; the arg-parsing shell stays separately testable.
- No TODOs, no silent fallbacks, no new dependencies (manifest unchanged),
  no OpenAPI surface changes.

## Gate results (exact commands, final code state)

| Gate | Command | Result |
|---|---|---|
| Format | `gofmt -l .` | PASS (no output) |
| Vet | `go vet ./...` | PASS |
| Lint | `go run honnef.co/go/tools/cmd/staticcheck@2026.2.1 ./...` | PASS (0 findings; cache redirected via `STATICCHECK_CACHE` because this sandbox cannot write `~/Library/Caches`) |
| Tidy idempotence | `go mod tidy && git diff --exit-code -- go.mod go.sum` | PASS (no changes) |
| Manifest | `go run ./scripts/manifestcheck` | PASS (no new dependencies) |
| Unit | `go test ./...` | PASS (all packages) |
| Race | `go test -race ./...` | PASS (benign macOS LC_DYSYMTAB linker warnings only) |
| Focused stress | `go test -race -count=60 ./internal/store/storetest/ ./internal/outboxgc/ ./internal/outboxops/ ./internal/cli/ ./internal/store/memory/` | PASS (60 reps of all fencing/concurrency/retention suites) |
| PG16 integration | `./scripts/test-postgres.sh` (disposable postgres:16-alpine; migrate up 0001–0008 + full contract suite incl. `RunOutboxRetention` and PG-specific retention tests) | PASS |
| Full CI | `make ci` | PASS (fmt-check, vet, lint, tidy-check, manifest-check, test, test-race, build, contracts-test, contracts-drift) |
| Contracts drift | via `make ci` (`contracts-drift`) | PASS — OpenAPI/schema.d.ts untouched by this tranche |
| Build | `make build` | PASS (bin/ants, bin/ants-api) |
| Demo | `make demo` | PASS |
| Live e2e smoke | disposable ants-smoke-pg (postgres:16-alpine), real `bin/ants` binary; scratch under repo-local `.local/`, removed afterwards; container removed afterwards | PASS (all checks listed below) |

## Live end-to-end smoke (all PASS)

Provisioned a fresh disposable `ants-smoke-pg` container and drove the real
binary through:

- `ants migrate up` applied 0001–0008 (0008 last);
- SQL-seeded production-shaped fixtures: three 30-day-old delivered rows,
  one discarded row (generation 2, dead_at/discarded_at set), one fresh
  delivered row, one pending row, one dead letter with full provenance;
- preview reported exactly `delivered=3 discarded=1` with store cutoff;
- unconfirmed `sweep` refused (exit != 0, refusal text names `--yes`), psql
  verified all four aged rows untouched;
- confirmed `sweep --yes` deleted exactly the four victims; fresh/pending/
  dead survivors verified row-by-row;
- immediate rerun was idempotent (JSON zeros);
- four concurrent sweeps over ten seeded aged rows summed to exactly ten
  deletions with no double counting (SKIP LOCKED partitioning) and the
  survivor set converged correctly;
- dead-letter list/show still operate post-0008 (regression);
- `serve` with `delivered_after: 1ns, interval: 1s`: the scheduled loop
  collected newly seeded rows without operator action, `/metrics` exposed
  `ants_outbox_retention_deleted_total{state="delivered"}` and
  `ants_outbox_retention_rounds_total`, and SIGTERM shut down cleanly with
  exit code 0.

All scratch files lived under repo-local `.local/` (gitignored this tranche)
and were deleted; the container was removed afterwards.

## Rollback / recovery implications

- Migration 0008 adds two indexes only — no columns change; rolling back the
  code leaves harmless indexes behind (forward-only schema convention; no
  downgrade path shipped or needed).
- Deletion is permanent: swept rows are not recoverable through the product.
  Domain history is unaffected — every swept delivery's event remains in
  `events`, operator interventions remain in `audit_events`.
- Each round is atomic (one unit of work on PostgreSQL; write lock in
  memory). A crashed process leaves either the whole round applied or none
  of it; reruns converge because deletion is idempotent.
- Inert-by-default means reverting configuration immediately stops future
  collection; already-deleted rows stay deleted.

## Known limitations

- Dead letters are never collected by design; a deployment whose dead-letter
  queue grows forever needs operator triage (requeue/discard), after which
  discarded rows age out normally.
- Preview numbers are advisory: between preview and sweep other actors can
  change the store; each sweep reports its own truthful counts.
- Legacy pre-migration rows with NULL terminal timestamps are retained
  forever unless corrected by direct SQL (documented fail-safe, exercised by
  a PG-specific test).
- Per-tenant retention horizons are not configurable yet; rounds are global.
- Metrics count successful rounds and deletions inline at the service seam;
  failed rounds surface in logs and CLI errors only.

## NOT RUN / BLOCKED

None. Every gate above executed to completion in this session against the
final code state. Push/PR steps were performed immediately after this record
was written.

## Prompt for PR 3.4 (next tranche, from the master plan)

"You are the sole coding agent for Ants Tranche 3 / PR 3.4. Work in
/Users/francescogiannicola/Documents/Codex/2026-08-22/vo/outputs/ants,
repository metaforismo/ants. Main must be clean at the squash merge of PR
#17 (feat/outbox-retention-gc); verify status, fetch, exact HEAD, AGENTS.md,
docs/MASTER_PLAN.md, docs/RESOURCES.md, ADR-0011, ADR-0013, ADR-0014,
ADR-0015, ADR-0016, docs/TRANCHE_3_3_EVIDENCE.md, and current code before
changing anything. Create a small branch for the tranche. Use English
throughout.

Deliver exactly one bounded outcome: multi-instance-safe operational
hardening of the durable subsystems' observable seams, per MASTER_PLAN
sections 15/21 — specifically (1) alerting-ready baselines documented over
the existing closed metric set (dead-letter growth, dispatch failures,
retention activity, 5xx rate) in the operations runbooks, with no new
instruments unless reviewed as another ADR-0014 amendment; (2) structured,
redacted request logging middleware on the API listener with fixed field
names and correlation IDs matching the event trace-id vocabulary; and (3)
graceful-shutdown convergence proof: an automated chaos-style test that
restarts `serve` mid-dispatch and proves at-least-once delivery converges
with no duplicated logical effects. Do NOT invent multi-node leader
election, remote operator APIs, OpenTelemetry tracing, or batch
administration. Follow the same rules as PR 3.3: grill ambiguous semantics
against the plan/ADRs into an explicit ADR if boundaries move; typed errors;
tenant-safe behavior; store-owned clock; forward-only migrations; no new
dependencies without manifest+ADR justification; deslop pass limited to the
tranche diff; gates = gofmt/vet/staticcheck/tidy/manifest/unit/race/focused
stress/PG16 integration/make ci/build/demo plus a real end-to-end smoke in a
disposable PG16 container using only repo-local ignored scratch files with
cleanup; record PASS/FAIL/BLOCKED honestly in docs/TRANCHE_3_4_EVIDENCE.md
and update the README pointer. Commit coherently, push, open a PR against
main with a detailed English body linking evidence, stop before merge, and
report the PR URL, head SHA, all gates, limitations, and a specific prompt
for PR 3.5."
