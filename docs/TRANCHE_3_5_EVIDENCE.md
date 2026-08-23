# Tranche 3 / PR 3.5 — Request correlation propagation: one identifier across response, log, events, and audit — evidence record

Date: 2026-08-23
Branch: `feat/request-event-correlation` → PR against `main`
Base: `main` @ a15b78b ("feat: redacted request logging, alerting
baselines, restart-convergence proof (ADR-0017) (#18)"), verified clean and
equal to `origin/main` before work began (`git status` empty, fetch exit 0,
HEAD match).
Environment: macOS arm64, Go toolchain (staticcheck 2026.2.1 auto-switches
to go1.26.7), Docker postgres:16-alpine (disposable), pnpm/openapi-typescript
7.13.0.

Scope: exactly one bounded outcome per the Tranche 3.4 handoff prompt and the
deferral named by ADR-0017 — the effective request correlation id flows
through the application seam so every event and audit record persisted while
serving that HTTP request carries it in its existing `trace_id` slot.
Delivered as ADR-0018 (`docs/adr/0018-request-correlation-propagation.md`),
written and accepted before implementation. No OpenTelemetry, no span trees,
no new dependencies, no migrations, no metric changes, no OpenAPI surface
change (`trace_id` was already in the Event schema).

## Requirement → code → test matrix

| # | Requirement | Code | Tests |
|---|---|---|---|
| R1 | Contract grilled and recorded before implementation | `docs/adr/0018-…md`: typed carrier, trust boundary (middleware is the only producer, post-validation), propagation semantics, explicit-beats-ambient precedence, non-HTTP fallback (empty ids, never fabricated), audit/event consistency, transaction behavior, explicit non-goals | human review vs MASTER_PLAN §6.3/§11.3/§15.1/§15.3, ADR-0004/0010/0011/0015/0017 |
| R2 | Smallest typed application-level carrier; set after resolution; retrievable without server imports or cycles | `internal/correlation` leaf package (imports stdlib only): `ID`, `MaxLength`, `Valid` (THE grammar), `With`/`From` (unexported context key, the ADR-0010 pattern), `TraceID(ctx, explicit)` encoding the precedence rule in one place. Middleware inserts the carrier immediately after `resolveCorrelation`, before auth dispatch — zero handler signature churn | `TestContextRoundTrip`, `TestTraceIDPrecedence`, `TestValidGrammar`; dependency direction proven by `go vet ./...` + build graph (correlation imports nothing internal) |
| R3 | Effective request id propagates into every existing event/audit record of HTTP-triggered commands | Exactly three emission seams fill the existing slot via `correlation.TraceID`: `orchestration.Engine.emitEvent` (all engine-emitted events incl. StartRun's idle→planning transition), server `emitTenantEvent`, policy `appendAudit`. No new records invented for paths that have none | `TestCorrelationFlowsIntoPersistedEvents` (generated / external verbatim / malformed-replaced: header == log == event row), `TestStartRunCarriesCorrelationIntoTransitionEvent`, `TestWorkerExecutionKeepsNoRequestIdentity` (full pipeline: planning transition carries the run-start id; every worker-phase event keeps `""`) |
| R4 | Denied/failure paths observe the same identity where durable records exist; no fabricated audit on denial | Carrier is set before auth dispatch, so denials echo/log the same id; no denial writes audit records (problem + request log remain the trail); policy denials during execution keep writing denied-result audits under their own context's rule | `TestAuthDenialCarriesCorrelationWithoutDurableRecords` (401 echoes header id, log matches, zero events/audit rows written); policy `TestAuditTraceIDFollowsRequestCarrier` (served vs background contexts) |
| R5 | One grammar; no drift between header acceptance, event trace ids, audit correlation, operator `--trace-id` | Grammar lives once in `correlation.Valid`; middleware consumes it (local copy deleted); `ports.OutboxMutationRequest.Validate` rejects non-empty trace ids failing the grammar → typed `outbox_trace_id` invalid error enforced by BOTH store adapters before any write | `TestOutboxMutationRequestValidatesTraceIDGrammar` (valid pass-through incl. previously documented forms; malformed/oversized rejected with stable code); smoke step 9 (CLI rejects `"bad id"` with zero audit writes) |
| R6 | Non-HTTP callers keep documented behavior, never a fabricated HTTP identity | Worker execution, dispatch, retention, demo, plain CLI run without a carrier → empty trace ids; outboxops takes provenance exclusively from explicit operator input (ambient can never override) | `TestWorkerExecutionKeepsNoRequestIdentity` (run created over HTTP with an id; all execution-phase events empty); policy background-context assertion; restart suite seeds prove store-level fidelity independent of any HTTP path |
| R7 | Transaction behavior: rollback leaves no orphan/mismatched records; redelivery replays the published correlation unchanged | Correlation only fills a field on records already atomic under UoW/outbox (ADR-0010/0011); envelopes serialize once at publish | `TestFailedEventUnitLeavesNoMismatchedCorrelation` (scripted event-store outage → 503, no tenant/event/delivery rows; recovery stamps the surviving event with ITS OWN attempt's id, failed attempt's id absent); PG `assertCorrelationSurvivesRestart` |
| R8 | Correlation survives durable outbox persistence, process death/redelivery; final history unchanged | Restart-convergence suite extended: each seeded event carries its own correlation id; after SIGKILL mid-dispatch + epoch-two drain, both durable copies must match byte-for-byte — `events.trace_id` AND `outbox.envelope->>'trace_id'` (NULL coalesced for omitempty) | `TestOutboxDeliveryConvergesAcrossProcessRestart` PASS 3.50s race-enabled PG16; broken variant (seeding empty traces) fails loudly three ways — "exactly 6 events must carry their seed correlations, got 0", per-event `trace_id drifted`, and per-event `redelivered envelope … lost its correlation` — assertions are regression-meaningful |
| R9 | Exhaustive HTTP integration proofs incl. concurrency and cross-tenant negatives | `internal/server/correlation_propagation_test.go`: real client requests against the full composition root | concurrency: 12 parallel creations each persisting its own id with clash detection; cross-tenant: per-tenant lists contain only their own correlations; malformed bytes provably absent from durable records |
| R10 | Live-binary disposable-PG smoke: header/log/event equality + redaction + clean SIGTERM | `.local/tranche-3_5/smoke-3_5.sh` (real `bin/ants`, real migrations, real dispatcher, scratch in gitignored `.local/tranche-3_5`, container removed afterwards) | 17/17 checks PASS (list below), including envelope-level equality and CLI `--trace-id` landing in both `events.trace_id` and `audit_events.trace_id` |

## Smoke checklist (all PASS)

disposable postgres ready · migrations applied · serving · creation 201 ·
healthz echoes correlation header · persisted event trace_id equals sent
header · outbox envelope carries same correlation · request log carries
header-sourced id · sentinels absent from logs · malformed id replaced,
creation succeeds · generated event trace has req_ prefix · log echoes
generated identity truthfully · CLI requeue with --trace-id succeeded ·
audit record carries operator trace id · operator event carries same trace
id · malformed --trace-id rejected with no writes · SIGTERM exits 0.

## Gate results (exact exit codes, final code state)

Exit codes captured directly from each command into repo-local ignored logs;
never inferred from piped output.

| Gate | Command | Exit |
|---|---|---|
| Format | `gofmt -l $(git ls-files '*.go')` | 0 (no output; scoped to tracked files because unrelated pre-existing scratch from other sessions sits under gitignored `.local/`) |
| Vet | `go vet ./...` | 0 |
| Lint | `STATICCHECK_CACHE=$PWD/.local/tranche-3_5/staticcheck-cache go run honnef.co/go/tools/cmd/staticcheck@2026.2.1 ./...` | 0 (after fixing SA1029 in a new test: anonymous context key → defined type) |
| Tidy idempotence | `go mod tidy && git diff --exit-code -- go.mod go.sum` | 0 (no changes) |
| Manifest | `go run ./scripts/manifestcheck` | 0 ("all direct dependencies are documented") |
| Unit | `go test ./...` | 0 (22 packages ok) |
| Race | `go test -race ./...` | 0 (22 packages ok) |
| Focused stress | `go test -race -count=60 ./internal/server/ ./internal/outbox/ ./internal/store/storetest/ ./internal/store/memory/ ./internal/correlation/` | 0 (server suite ×60 = 80.5s) |
| PG16 integration | `./scripts/test-postgres.sh` (disposable postgres:16-alpine; migrations 0001–0008 + contract suites + extended restart-convergence test) | 0 (postgres package 12.1s includes the restart test) — re-run on the exact final tree |
| Restart test, verbose | `TEST_PG_DSN=… go test -count=1 -race -v -run TestOutboxDeliveryConvergesAcrossProcessRestart ./internal/store/postgres/` | 0 (3.50s; "epoch 1 crashed inside the sink as designed: signal: killed", "epoch 2 drained and exited 0") |
| Broken variant | same command with seeding patched to empty traces | 1 (FAIL as designed; reverted) — proves the new assertions detect drift |
| Full CI | `STATICCHECK_CACHE=$PWD/.local/tranche-3_5/staticcheck-cache make ci` | 0 (fmt-check, vet, lint, tidy-check, manifest-check, test, test-race, build, contracts-test, contracts-drift) |
| Contracts drift | via `make ci` (`contracts-drift`) | 0 — OpenAPI/schema.d.ts untouched |
| Build | `make build` | 0 (bin/ants, bin/ants-api) |
| Demo | `make demo` | 0 |
| E2E smoke | `.local/tranche-3_5/smoke-3_5.sh` | 0 (17/17 checks, container + config cleaned; logs retained until evidence recorded, then removed) |

## Audit/deslop findings this session

- **D1 — grammar moved, not wrapped:** the initial edit kept a
  `validCorrelationID` one-line wrapper plus an alias constant over the
  correlation package; both deleted per no-indirection rules, callers use
  `correlation.Valid`/`correlation.MaxLength` directly. The server-level
  grammar table test was deleted rather than duplicated — the table now
  lives in the package that owns the function.
- **D2 — local struct replaced by typed return:** the middleware's
  `correlation{id,source}` struct was removed; `resolveCorrelation` returns
  `(correlation.ID, string)` directly, eliminating the name shadow between
  struct and package and one allocation-shaped indirection.
- **D3 — redundant setup removed:** the StartRun propagation test initially
  created a project manually although `seedProjectThread` creates its own;
  dropped.
- **D4 — staticcheck finding fixed:** derived-context isolation check used
  an anonymous empty struct as a context key (SA1029); replaced with a
  defined key type inside the test.
- **D5 — NULL-envelope robustness:** the restart assertion initially scanned
  `envelope->>'trace_id'` raw; envelopes omit empty traces (domain field is
  `omitempty`), so a legitimately-empty trace produced a confusing scan
  error instead of drift reporting. Coalesced to `''`; found by running the
  broken variant, not by review.
- No TODOs, silent fallbacks, new dependencies (manifest unchanged), config
  additions, OpenAPI changes, or metric changes. Zero production wrapper
  types introduced; the four production seams are one line each.

## Operator-visible changes

- Events and audit records written while serving an HTTP request now carry
  the request's effective correlation id in their existing `trace_id`
  column/JSON field. Consumers joining logs to events gain exact equality;
  consumers assuming always-empty trace ids will now see values on
  request-scoped rows (the documented purpose of the slot).
- `ants outbox dead-letter requeue|discard --trace-id` now rejects values
  outside the shared grammar with a typed invalid request instead of
  persisting arbitrary bytes. All grammar-compatible values behave exactly
  as before (including everything previously documented).
- Rollback is a pure revert: no schema change, no config surface, no stored-
  data format change (values land in pre-existing columns).

## Known limitations

- Only two emission points are synchronous to HTTP today (tenant creation,
  StartRun's planning transition); policy-evaluation audits occur during
  worker execution and therefore carry empty trace ids by design. The
  mechanism covers future request-scoped seams automatically via the same
  one-line pattern.
- Cross-time attribution (which request caused a run executed later by the
  worker) is deliberately NOT modeled: smuggling the request id into
  worker-phase events would fabricate identity (ADR-0018 alternatives). If
  needed later it requires an explicit field such as `requested_by_trace`.
- The API exposes run-scoped events only; request-correlated thread/tenant
  transitions are verifiable through persistence and the durable envelopes
  (as tested), not yet through a tenant-wide events endpoint.
- Correlation joins remain within one Ants deployment; W3C traceparent /
  OTel export stay deferred (ADR-0017/0018 non-goals).

## NOT RUN / BLOCKED

None. Every gate above executed to completion against the final code state
in this session; containers were removed (`docker ps` shows no `ants-*`
containers) and scratch/config files under `.local/tranche-3_5/` are
gitignored and removed after evidence capture.

## Independent audit (2026-08-23, head ac76b1d)

A second agent re-audited the branch against the ADRs and re-executed the
gates from a clean tree (scratch in gitignored `.local/audit-3_5`):

- **Carrier safety:** unexported typed context key, stdlib-only leaf
  package, single grammar copy, precedence encoded once — verified against
  the full build graph. No globals, cycles, or server imports.
- **Seam completeness:** production-wide enumeration found exactly three
  event appenders (engine `emitEvent`, server `emitTenantEvent`, outboxops
  with explicit operator provenance) and two audit appenders (policy
  engine, outboxops); every HTTP route is wrapped by the carrier-producing
  middleware; only tenant creation and StartRun commit records
  synchronously.
- **Fixes landed in ac76b1d:** ADR-0018/README/SECURITY wording corrected
  to stop claiming end-to-end HTTP audit equality (no synchronous audit
  seam exists); worker-isolation test now counts occurrences per trace so a
  multi-event identity stamp fails instead of hiding behind map dedup.
- **Broken variant re-run on final code** (seeds patched to empty traces,
  then reverted): exit 1 with all three assertion classes firing — count
  0 ≠ 6, per-event trace drift, per-event envelope drift. The R8 row above
  was corrected accordingly: the previously recorded "NULL scan error"
  described the pre-D5 assertion shape, not the final coalesced one.

Re-execution at head `ac76b1d`, all logs under gitignored
`.local/audit-3_5/` (exit codes captured directly from each command):
gofmt(tracked) 0 · vet 0 · staticcheck@2026.2.1 0 · tidy diff-clean 0 ·
manifest 0 · unit 0 (22 packages ok) · race 0 (22 packages ok) · focused
stress `-race -count=60` over server/outbox/storetest/memory/correlation 0
(server suite 83.9s) · `scripts/test-postgres.sh` 0 (postgres package
13.0s incl. restart test; container removed by script) · verbose restart
test `-race -v` on disposable PG16 0 ("epoch 1 crashed inside the sink as
designed", "epoch 2 drained and exited 0", PASS 3.55s) · broken variant 1
(FAIL as designed, reverted) · `make ci` 0 · `make build`+`make demo` 0 ·
live-binary smoke rebuilt for `.local/audit-3_5/smoke-audit.sh`: real
`bin/ants serve`, migrations 0001–0008, real dispatcher — 17/17 checks
PASS, exit 0, including header==log==event==envelope equality for a
header-sourced id, CLI `--trace-id` landing in both `events.trace_id` and
`audit_events.trace_id`, malformed `--trace-id` rejected as typed
`outbox_trace_id` with zero writes, sentinels absent from logs, SIGTERM
exit 0. Smoke container and generated config removed afterwards.

## Prompt for PR 3.6 (next tranche, from the master plan)

"You are the sole coding agent for Ants Tranche 3 / PR 3.6. Work in
/Users/francescogiannicola/Documents/Codex/2026-08-22/vo/outputs/ants,
repository metaforismo/ants. Main must be clean at the squash merge of the
request-event-correlation PR; verify status/fetch/exact HEAD and read
AGENTS.md, docs/MASTER_PLAN.md, docs/RESOURCES.md, docs/TRANCHE_3_5_EVIDENCE.md,
ADR-0004, ADR-0013, ADR-0015, ADR-0017, ADR-0018, and current code first.
Create a small branch; English throughout.

Deliver exactly one bounded outcome chosen from MASTER_PLAN Horizon 1 item 2
('OIDC locale reale') — the completion condition ADR-0004 has named since
tranche 1 ('full removal still lands with OIDC') and ADR-0013 reinforced:
production-grade authentication replaces the development posture at the
existing `server.Authenticator` seam. Scope: OIDC Authorization Code + PKCE
login against Keycloak running as a disposable local container (Apache-2.0,
per the OSS atlas adopt decision), verified-ID-token sessions for /v1
routes, tenant resolution from verified claims instead of self-declared dev
headers, and startup validation that removes the dev-header path's ability
to authenticate anything but an explicit loopback-only development profile —
or deletes it outright if wiring permits without breaking the demo/e2e
fixtures. Requirements: handlers do not change signature (the seam exists
for exactly this, ADR-0004); principal identity flows into the existing
audit and event actor fields; unauthenticated requests keep the uniform
typed problem contract; no new HTTP surface beyond the documented redirect
and callback endpoints added to the pinned route table AND openapi.yaml
together (contract test stays green); correlation propagation (ADR-0018)
must keep working unchanged through authenticated requests — prove header/
log/event equality under OIDC-authenticated traffic; secrets (client
secret, DSN) only via config.Secret/environment, never files or flags;
cancellation and timeouts on every token/network exchange; typed errors for
expired/invalid tokens distinct from not-configured. Do NOT implement
OpenFGA authorization tuples, service principals, device flow, token
refresh, multi-IdP config, or remote admin APIs — name them deferred.
Constraints: no paid services, no external network calls (Keycloak runs
locally in Docker like the PG suites); if Keycloak cannot run in this
environment, mark the live-path proof BLOCKED and still land the seam,
config validation, and unit/contract tests with honest boundaries. Same
gates and honesty rules as PR 3.5 (gofmt/vet/pinned staticcheck/tidy/
manifest/unit/race/focused count=60/PG16 integration incl. restart test/
make ci/build/demo/live-binary smoke against disposable containers with
repo-local ignored scratch and full cleanup); record PASS/FAIL/BLOCKED
honestly in docs/TRANCHE_3_6_EVIDENCE.md, update README pointer, SECURITY
posture (dev-header removal delta), runbooks, and ADR cross-links, commit
coherently, push, open one small PR against main linking evidence, stop
before merge, and report URL, head SHA, gates, limitations, and a concrete
PR 3.7 prompt."
