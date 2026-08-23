# Tranche 3 / PR 3.4 — Operational observability and restart hardening: redacted request logs, alerting baselines, restart-convergence proof — evidence record

Date: 2026-08-23
Branch: `feat/observability-restart-hardening` → PR against `main`
Base: `main` @ 017ecf6 ("feat: outbox retention/GC — bounded deletion of
terminal rows (ADR-0016) (#17)"), verified clean and equal to `origin/main`
before work began (`git status` empty, fetch exit 0, HEAD match).
Environment: macOS arm64, Go toolchain 1.25.x (staticcheck 2026.2.1
auto-switches to go1.26.7), Docker postgres:16-alpine (disposable),
pnpm/openapi-typescript 7.13.0.

Scope: one bounded Tranche 3 outcome per the 3.3 handoff prompt and ADR-0017:
(1) structured, redacted request logging middleware with a documented
correlation vocabulary; (2) alerting-ready runbook baselines and PromQL over
the existing closed metric set (zero new instruments); (3) an automated
chaos-style restart integration test proving at-least-once outbox delivery
converges across a real process kill with no duplicated logical effects.
No OpenTelemetry, no remote admin APIs, no multi-node leader election, no
auth stubs, no paid services, no new dependencies, no OpenAPI changes, no
migrations.

## Requirement → code → test matrix

| # | Requirement | Code | Tests |
|---|---|---|---|
| R1 | Semantics grilled against plan/ADRs and recorded before implementation | `docs/adr/0017-request-logging-and-restart-convergence.md` (field contract, correlation grammar and truthfulness rules, capability-probing recorder, panic consistency, restart-test semantics, explicit out-of-scope) | human review vs MASTER_PLAN §15.1/§15.3/§21, ADR-0004/0011/0013/0014/0015 |
| R2 | Request log carries only fixed low-cardinality fields: method, normalized route, status, duration, correlation id + source, remote class; never raw URL/query/headers/bodies/IDs/secrets/client addresses | `internal/server/middleware.go` — field set emitted in one place; route comes from the pinned pattern table (catch-all uses the same `unmatched` constant as metrics), so log and metric labels cannot disagree | `TestRequestLogsAreRedactedAndNormalized` (sentinel bearer/cookie/query/body/principal/thread-ID all absent; forbidden fields absent; normalized `/v1/threads/{id}` present); internal grammar tests |
| R3 | Correlation IDs generated/validated truthfully on the event trace-id vocabulary; documented response header echo | `resolveCorrelation` accepts inbound `X-Request-ID` iff it satisfies the shared grammar ([A-Za-z0-9._~:@-], ≤128), else generates via `domain.NewID(PrefixRequest)`; source logged (`header`\|`generated`); effective id echoed; entropy failure logs Error and marks every issued id `req_generation_failed`; `PrefixRequest` added to the contractual prefix table | `TestCorrelationIDAcceptanceGrammar` (8 cases end-to-end through a real HTTP client; CR/LF and control-byte injections cannot traverse a real client — their rejection is pinned at grammar level in `TestValidCorrelationID`); `TestValidCorrelationID`, `TestResolveCorrelationTruthfulSources`; smoke asserts echoed id equals logged id |
| R4 | Optional ResponseWriter interfaces preserved; streaming/flushing keep working | capability-probing `wrapWriter` returns plain/flush/hijack/full forwarding wrappers — no lying no-op methods; `Unwrap()` for `http.ResponseController` | `TestWrapWriterPreservesCapabilities` (4 subtests: no false Flusher/Hijacker claims, forwarding proven, Unwrap identity); `TestRecorderImplicitStatusIsOK` |
| R5 | Panics handled consistently inside the observability chain | recovery registered outermost so metrics+log observe final status; typed `internal` problem written only if response not started (no superfluous double-header after mid-stream panics); bounded panic value | `TestPanicBeforeResponseBecomesTypedProblem` (500 problem + correlated records + real status observed); `TestPanicAfterResponseStartDoesNotDoubleWrite`; `TestPanicValueIsBoundedInLogs` |
| R6 | Remote classification bounded and privacy-preserving | `remoteClass`: loopback/private/public/unknown via netip (zone-qualified v6 handled); address itself never logged | `TestRemoteClassVocabulary` (v4/v6/zones/malformed/no-port) |
| R7 | Concurrency safety of the chain | state confined per-request; single deferred emission | `TestConcurrentRequestsKeepDistinctCorrelations` (16×12 mixed goroutines; every echoed id logged exactly once, none crossed); full `-race` gate |
| R8 | `/metrics` cardinality posture unchanged | zero instruments added or altered; logging reuses the metric route vocabulary | `TestMetricsCardinalityUnchangedByLogging`; `make ci` contracts-drift untouched; exposition pinned series asserted in smoke |
| R9 | Alerting-ready baselines + PromQL over the closed set with denominators/windows/caveats; deployment-specific thresholds marked as starting points | `docs/runbooks/alerting-baselines.md` (dead-letter growth, retry share with well-formed denominator, dispatch stall, retention activity/stall with configured-guard, 5xx ratio + low-traffic absolute form, readiness probe guidance, worker converged/starvation signals, saturation, full series inventory); outbox runbook alerting notes now link to it | expressions reference ONLY ADR-0014-set series (inventory table cross-checked against `internal/metrics/metrics.go`); no code change → nothing to unit-test; doc review |
| R10 | Automated chaos-style restart test: disposable PostgreSQL, real process lifecycle, at-least-once convergence, redelivery ≠ duplicated logical effects, deterministic and deadline-bounded | `internal/store/postgres/restart_convergence_pg_test.go` — epoch 1 runs as a separate OS process (helper exec of the test binary) driving the REAL dispatcher over the REAL PostgreSQL adapter; harness sink records attempt ledger + applies effects idempotently (`ON CONFLICT DO NOTHING`); SIGKILL is delivered from inside the sink immediately after effect commit, deterministically creating effect-applied/unacknowledged deliveries; epoch 2 drains over lease-expiry reclaim; all waits poll DB state under deadlines | `TestOutboxDeliveryConvergesAcrossProcessRestart` (PASS 3.54s race-enabled against PG16; asserts all rows delivered, min attempts ≥ 2 both epochs, attempts strictly > events, effects == seeded exactly with per-event uniqueness, unknown-event effects impossible, durable events untouched); broken-variant proof below |
| R11 | Test-only harness isolated and production-safe; production-vs-harness boundary documented | everything lives in `_test.go`; the only substitution is the Sink — the documented consumer extension point (ADR-0011); no debug endpoints, no config surface, no production wiring; boundary stated in file header comment and ADR-0017 | code inspection; production tree contains no harness symbols (`grep` clean) |
| R12 | Docs: README/latest pointer, SECURITY posture, runbooks, evidence honesty | README status + pointer swap; SECURITY structural-guarantee entry for request-log redaction; runbook updates; this record | links resolve; commands below copy-paste reproducible |

## Restart test timeline (single deterministic scenario)

```text
t0  hold=TRUE armed; epoch-1 process starts (own pool, own worker identity),
    dispatches into the scripted outage; every message fails and backs off.
t1  parent observes ≥6 failed attempts in the ledger (poll, deadline 20s).
t2  parent arms die_on_effect. Next successful delivery commits its logical
    effect and the sink SIGKILLs its own process BEFORE the ack can land:
    durable effect + leased + unacknowledged, by construction.
t3  parent reaps epoch 1 ("signal: killed"), verifies ≥1 pre-crash effect.
t4  die_on_effect disarmed; epoch-2 process starts fresh over the wreckage.
    Effected-but-unacked messages are reclaimed at lease expiry (~≤2s,
    lease = production minimum) and delivered again; the idempotent sink
    absorbs them. Remaining messages deliver normally.
t5  convergence polled (deadline 30s): 0 undelivered, 6 delivered, 6 effects.
t6  SIGTERM to epoch 2 → graceful drain → exit 0 (production signal path).
t7  state assertions (see R10).
```

Typical wall time 3–4 s under `-race`. No fixed sleeps drive any outcome;
poll intervals exist only to schedule deadline-bounded checks.

## Broken-variant proof (regression meaningfulness)

With the sink's dedup guard removed (`ON CONFLICT … DO NOTHING` deleted AND
the effects table primary key dropped — i.e., a non-idempotent consumer),
the same scenario produces a genuine duplicated logical effect and the test
fails:

```text
restart_convergence_pg_test.go:227: epoch 1 crashed inside the sink as designed: signal: killed
restart_convergence_pg_test.go:243: outbox did not converge in time: undelivered=0 rows=6 effects=7 (want 6 delivered, 6 effects)
FAIL    github.com/metaforismo/ants/internal/store/postgres     30.859s
```

Seven effects for six seeded events — the redelivery re-applied an already-
applied effect and the assertions caught it. With the idempotent sink
restored, the identical command exits 0. An earlier draft of the scenario
(kill during the held-outage phase only) PASSED the broken variant because
no effected work was ever redelivered — it was redesigned before landing
precisely so the proof would be meaningful.

## Gate results (exact exit codes, final code state)

Exit codes captured directly from each command, never inferred from piped
output; logs captured outside pipes where redirection was used.

| Gate | Command | Exit |
|---|---|---|
| Format | `gofmt -l .` | 0 (no output) |
| Vet | `go vet ./...` | 0 |
| Lint | `STATICCHECK_CACHE=$PWD/.local/staticcheck-cache go run honnef.co/go/tools/cmd/staticcheck@2026.2.1 ./...` | 0 (cache redirected; sandbox cannot write `~/Library/Caches`) |
| Tidy idempotence | `go mod tidy && git diff --exit-code -- go.mod go.sum` | 0 (no changes) |
| Manifest | `go run ./scripts/manifestcheck` | 0 (no new dependencies) |
| Unit | `go test ./...` | 0 (all packages) |
| Race | `go test -race ./...` | 0 |
| Focused stress | `go test -race -count=60 ./internal/server/ ./internal/outbox/ ./internal/store/storetest/ ./internal/store/memory/` | 0 (server suite ×60 = 62.7s) |
| PG16 integration | `./scripts/test-postgres.sh` (disposable postgres:16-alpine; migrations 0001–0008 + full contract suites + restart-convergence test) | 0 (postgres package 11.8s includes the restart test) |
| Restart test, verbose | `TEST_PG_DSN=… go test -count=1 -race -v -run TestOutboxDeliveryConvergesAcrossProcessRestart ./internal/store/postgres/` | 0 (3.54s; "epoch 1 crashed inside the sink as designed: signal: killed", "epoch 2 drained and exited 0") |
| Full CI | `STATICCHECK_CACHE=$PWD/.local/staticcheck-cache make ci` | 0 (fmt-check, vet, lint, tidy-check, manifest-check, test, test-race, build, contracts-test, contracts-drift) |
| Contracts drift | via `make ci` (`contracts-drift`) | 0 — OpenAPI/schema.d.ts untouched |
| Build | `make build` | 0 (bin/ants, bin/ants-api) |
| Demo | `make demo` | 0 |
| E2E smoke | `.local/smoke-3_4.sh` (real binary, disposable ants-smoke-pg-34, scratch under gitignored `.local/`, container + scratch removed afterwards) | 0 (19/19 checks, list below) |

### E2E smoke checklist (all PASS)

migrations 0001–0008 applied · epoch-1 serving · tenant creation 201 ·
response echoes generated `req_…` id · request log carries
route/status/correlation-source/remote-class truthfully · bearer sentinel
absent · cookie sentinel absent · query sentinel absent · no client address
· exactly one pending delivery before crash · delivery still pending after
SIGKILL · epoch-2 serving · delivery converged to `delivered` post-restart
(attempts=1) · epoch-2 creation feeds fresh metrics · `/metrics` serves
`ants_http_requests_total{method="POST",route="/v1/tenants",status="201"}`
· well-formed external correlation id accepted · SIGTERM exits 0.

## Audit/deslop findings this session

- **D1 — first restart-scenario design could not fail on the broken variant
  (fixed before landing).** Killing epoch 1 during the held-outage phase
  meant no effected work was ever redelivered, so sink non-idempotency went
  undetected (broken variant passed). Redesigned to crash the process from
  inside the sink right after effect commit, forcing the
  applied-but-unacknowledged window that idempotency exists for. Both
  variant outcomes captured above.
- **D2 — entropy-failure path was silently quiet (fixed):** generation
  failure produced the `req_generation_failed` marker without the promised
  loud signal; `resolveCorrelation` now takes the logger and emits an Error
  record when it fires.
- **D3 — removed an unused parameter** (`terminateRestartEpoch`'s always-
  SIGKILL signal argument) and a redundant duplicate header lookup in
  `resolveCorrelation`.
- **D4 — middleware indirection removed:** `Server.wrap` was a one-caller
  wrapper around `withRequestLog(recoverPanics(…))`; routes now call the
  single observability entry point directly, and the old raw-path/panic-log
  fields have no remaining code path.
- No TODOs, no silent fallbacks, no new dependencies (manifest unchanged),
  no OpenAPI surface changes, no config additions (the logging contract is
  unconditional, like the health probes).

## Operator-visible changes and rollback

- **Log format change (intentional):** request records replace `path` with
  the normalized `route` and gain `correlation_source`/`remote_class`;
  `request_id` values are now grammar-validated (previously unbounded echo).
  Log consumers keyed on `path=` must switch to `route=` patterns in the
  same deploy. Panic records follow the same contract.
- Rollback is a pure revert: no schema change, no config surface, no metric
  changes, no stored data affected. The `req` prefix joins the ID prefix
  table but no existing identifier form changes.

## Known limitations

- Request correlation ids do not yet propagate into emitted domain events'
  `trace_id` — vocabularies align and external ids flow in, but plumbing
  correlation through the engine into envelopes is deferred (ADR-0017) and
  is the natural next step.
- `remote_class` is derived from the peer address; deployments behind a
  proxy see the proxy's class unless proxy protocol support lands later.
- The restart proof covers single-process dispatch loss including the
  effect-applied/unacknowledged window and lease expiry; multi-instance
  dispatcher scale-out remains explicitly deferred (ADR-0011/0013).
- Alert baselines are documentation: evaluation/scheduling infrastructure
  (Prometheus rules files, Alertmanager) is not part of this tranche.
- The e2e smoke observes convergence via outbox row states (the production
  LogSink has no external effects); logical-effect identity is proven by the
  automated test's harness sink instead.

## NOT RUN / BLOCKED

None. Every gate above executed to completion against the final code state
in this session; containers and repo-local scratch were removed afterwards
(`docker ps` shows no `ants-*` containers; `.local/` cleaned).

## Prompt for PR 3.5 (next tranche, from the master plan)

"You are the sole coding agent for Ants Tranche 3 / PR 3.5. Work in
/Users/francescogiannicola/Documents/Codex/2026-08-22/vo/outputs/ants,
repository metaforismo/ants. Main must be clean at the squash merge of the
observability/restart-hardening PR; verify status/fetch/exact HEAD and read
AGENTS.md, docs/MASTER_PLAN.md, docs/RESOURCES.md, ADR-0011, ADR-0013,
ADR-0014, ADR-0015, ADR-0016, ADR-0017, docs/TRANCHE_3_4_EVIDENCE.md, and
current code first. Create a small branch; English throughout.

Deliver exactly one bounded outcome: end-to-end correlation propagation
(ADR-0017's named deferral, implementing MASTER_PLAN §6.3 'trace id' on
transitions and §11.3 envelope trace_id): the request correlation id from
`X-Request-ID` flows through the application seam into the orchestration
engine so domain events and audit records persisted while serving that
request carry it in their existing `trace_id` slot; CLI operator actions
already pass `--trace-id` and must keep working unchanged. Requirements:
context-carried correlation with zero handler signature churn; events
written outside any request (worker, dispatcher, retention) keep empty or
operator-supplied trace ids; no behavior change on redelivery; contract
test pins envelope trace_id round-trip through API-created transitions;
negative tests prove cross-request isolation and that cancellation paths
still persist their terminal writes; extend the restart-convergence suite
to assert propagated trace ids survive process death. Do NOT add
OpenTelemetry export, span trees, remote APIs, or new dependencies; the
closed metric set must not grow. Same gates and honesty rules as PR 3.4
(gofmt/vet/pinned staticcheck/tidy/manifest/unit/race/focused count=60/
PG16 integration incl. restart test/make ci/build/demo/e2e smoke in a
disposable container with repo-local ignored scratch and cleanup);
record PASS/FAIL/BLOCKED honestly in docs/TRANCHE_3_5_EVIDENCE.md, update
README pointer and ADR-0017's deferral note. Commit coherently, push, open
one small PR against main linking evidence, stop before merge, and report
URL, head SHA, gates, limitations, and a concrete PR 3.6 prompt."
