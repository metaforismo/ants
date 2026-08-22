# Tranche 3 / PR 3.1 — Production-grade Prometheus metrics platform: evidence record

Date: 2026-08-23
Branch: `feat/metrics-platform` → PR against `main`
Base: `main` @ 799017b ("audit: tranche 2 closure — atomic tenant event,
honest run reads, truthful docs, final evidence (#14)")
Environment: macOS 26.x, Apple Silicon (arm64), Go 1.25.5 toolchain
(staticcheck 2026.2.1 auto-switched to go1.26.7), Docker (disposable
postgres:16-alpine container), pnpm/openapi-typescript 7.13.0.

Scope: outcome 1 of 7 of Tranche 3 (Operable durable core) only: the
Prometheus metrics platform per ADR-0014 — dependency adoption, the
`internal/metrics` collector set, config gating, `/metrics` on the API
listener, consumer-side observer seams for the outbox dispatcher and run
worker, composition wiring, contract regeneration, and deterministic tests.
No dead-letter tooling, retention/GC, or any other Tranche 3 outcome was
started.

## Requirement → code → test matrix

| # | Requirement | Code | Tests |
|---|---|---|---|
| R1 | Instance-owned registry; global default never used | `internal/metrics/metrics.go` `New()` builds `prometheus.NewRegistry()`; no package-level instruments, no `init()` | `TestRegistryExposesPromisedInstruments` |
| R2 | Closed instrument set per ADR-0014 (incl. reviewed addition `ants_outbox_messages_leased_total`) | same file, `MustRegister` list | `wantNames` pins every promised family incl. Go/process collectors |
| R3 | `ants_outbox_dispatch_rounds_total` counts **rounds**, never leased-message volume; leased volume lives in its own correctly named counter | `Metrics.RoundLeased`: rounds `.Inc()` once per call, `ants_outbox_messages_leased_total` `.Add(leased)` | `TestRoundCounterCountsRoundsNotMessages` (rounds=2, leased=3 after `RoundLeased(3)` + `RoundLeased(0)`) |
| R4 | State gauges have truthful sampling semantics | gauge help states sampling happens after each dispatch round's lease step; `OutboxStates` sets all four states from one store read | `TestGaugeValuesTrackLatestSample`; `TestObserverRecordsDeliveryLifecycle` (post-lease instant); idle-round sample in `TestObserverRecordsRetryAndDeadLetter` |
| R5 | Observer failures cannot affect business behavior | observers are void callbacks; the only fallible instrument feed (`store.Stats`) is logged-and-skipped on error (`dispatcher.go`); nil observer = identical behavior | `TestNilObserverKeepsDispatchBehavior`, `TestNilObserverKeepsExecutionBehavior`; every pre-existing dispatcher/worker test runs the nil path |
| R6 | Callback placement never overcounts when persistence fails | `Delivered` observed only after `MarkDelivered` succeeds; `RetryScheduled`/`DeadLettered` only after `FailWithBackoff` succeeds; worker `RunFinished` fires exactly once per execution whose reloaded run is terminal (claim-cleanup failure cannot un-finish a run); convergences observed only after their persistence succeeds; leftover-claim cleanup deliberately observes nothing | outbox/worker observer suites above; persistence-failure logging paths covered by pre-existing failure-injection tests |
| R7 | Labels are fixed bounded vocabularies only — no raw paths, IDs, tenants, principals, PII | HTTP: method × mux route pattern × status; unmatched requests share the constant `unmatched` route label (`metrics.RouteUnmatched`); outbox: state; worker: terminal outcome, convergence kind | `TestMetricsEndpointServesBoundedSeries` pins pattern-labeled series and the single `route="unmatched"` catch-all |
| R8 | `/metrics` on the API listener, config-gated; disabled means the route does not exist (uniform 404 problem), not a stub | `server.routes` skips registration when collector is nil | `TestMetricsDisabledRemovesRoute` + live checks below |
| R9 | Enabled metrics require a collector at construction; disabled allows none | `server.New` guard | `TestEnabledMetricsRequireCollector`, `TestDisabledMetricsAllowNilCollector` |
| R10 | HTTP edge instrumentation (volume, latency histogram, in-flight), counted also on panic-recovered 500s | `withRequestLog` wraps `recoverPanics` (order swapped in this PR so the recorder sees recovered 500s); in-flight inc/dec via defer | `TestMetricsEndpointServesBoundedSeries` (POST 201 series) |
| R11 | Route table ↔ OpenAPI ↔ TS contracts stay pinned | `/metrics` in `APIRoutes()`, regenerated `schema.d.ts` | `TestOpenAPIMatchesRoutes` (auto-covers new route), `make contracts-drift`, contracts package tests |
| R12 | Config: typed section, strict YAML, env override, unknown keys rejected | `config.Metrics{Enabled}`, `ANTS_METRICS_ENABLED`, registered in `knownEnvVars` | `TestMetricsYAMLSection`, `TestMetricsEnvLayering`, global unknown-key/env tests |
| R13 | Dependency governance: license, provenance, toolchain fit, minimal upgrades | client_golang v1.24.1 (Apache-2.0) in `third_party/manifest.yaml`; module requires Go ≥ 1.25.0, satisfied by `go 1.25.0` directive / go1.25.5 toolchain; go.mod changes are exactly the MVS closure (prometheus common/model/procfs/perks/xxhash/goautoneg/protobuf + golang.org/x bumps pulled transitively); no test-only dependencies added | `make manifest-check`; `go mod tidy` idempotence below |
| R14 | Worker observation coverage: acquisition, terminal outcomes, interrupted & exhausted convergence | `worker.Observer` seam implemented structurally by `*metrics.Metrics` | `TestObserverRecordsAcquisitionAndTerminalOutcome`, `TestObserverRecordsInterruptedConvergence`, `TestObserverRecordsExhaustedConvergence` |

## Gate results (exact commands)

| Gate | Command | Result |
|---|---|---|
| Format | `gofmt -l .` | PASS (no output) |
| Vet | `go vet ./...` | PASS |
| Lint | `go run honnef.co/go/tools/cmd/staticcheck@2026.2.1 ./...` | PASS |
| Tidy idempotence | `go mod tidy` then byte-compare go.mod/go.sum against pre-run copies | PASS (identical; re-run produced zero changes) |
| Manifest | `make manifest-check` | PASS (pgx MIT, client_golang Apache-2.0, yaml.v3 MIT) |
| Unit | `go test ./...` | PASS |
| Race | `go test -race ./...` | PASS (macOS ld LC_DYSYMTAB warnings only, benign) |
| Focused stress | `go test -race -count=3 ./internal/{metrics,outbox,worker,server}/` | PASS |
| PostgreSQL integration | `./scripts/test-postgres.sh` (disposable postgres:16-alpine) | PASS (migrate, postgres, storetest suites green) |
| Contracts | `pnpm --filter @ants/contracts test` | PASS (19/19) |
| Contract drift | regeneration byte-compared via SHA-256 before commit; `make contracts-drift` after staging | PASS |
| Build | `make build` | PASS (bin/ants, bin/ants-api) |
| Demo | `make demo` | PASS (2/2 tasks integrated, ready_for_review=true) |
| Live scrape enabled | serve on 127.0.0.1:18080; GET /healthz=200, GET /metrics=200 `text/plain` with `ants_http_requests_total`, `ants_outbox_dispatch_rounds_total`, `go_goroutines` | PASS |
| Clean SIGTERM (enabled) | `kill -TERM` → process exit code 0, "shutdown requested" logged | PASS |
| Live scrape disabled | `metrics.enabled: false` on 127.0.0.1:18081; GET /metrics = uniform RFC 9457 `route_not_found` problem, 404 | PASS |
| Clean SIGTERM (disabled) | exit code 0 | PASS |
| Full CI | `make ci` (run after staging the intentional go.mod/go.sum and schema.d.ts changes so tidy/drift gates compare truthfully) | PASS |

## Audit findings fixed during this session

1. **Metric semantics bug — FIXED:** `RoundLeased(n)` added the leased-batch
   size directly to `ants_outbox_dispatch_rounds_total`, so the counter
   counted leased messages instead of rounds. Split into round counting
   (+1/round) plus a new correctly named `ants_outbox_messages_leased_total`
   counter; ADR-0014's instrument list amended with the same reviewed intent;
   semantic pin added (`TestRoundCounterCountsRoundsNotMessages`).
2. **Panic-path undercount — FIXED:** `recoverPanics` sat outside the request
   log, so a panicked handler produced a served 500 that appeared in neither
   `ants_http_requests_total` nor the access log. Middleware order swapped;
   the status recorder now sees the recovered 500.
3. **Duplicated comment — FIXED:** `RoundLeased` doc comment was duplicated;
   replaced with a single comment stating the round/message semantic split.

## Known limitations

- Outbox state gauges sample once per dispatch round (immediately after the
  lease step), so delivered/dead counts lag by up to one poll interval. This
  is stated in the metric help text and pinned by tests; it keeps the
  dispatcher free of per-state write amplification.
- The retry-vs-dead-letter classification mirrors the log classification
  (`msg.Attempts >= msg.MaxAttempts`, attempts snapshot taken at lease time);
  metrics and logs can therefore never disagree, but both share that
  predicate's semantics.
- `/metrics` is unauthenticated by design, like `/healthz` and `/readyz`
  (ADR-0014 consequences; documented in SECURITY.md). Moving it behind a
  future ACL'd admin listener is one wiring change because the collector is
  injected, not global.
- Disabled metrics remove the route entirely; scrapers receive the uniform
  404 problem rather than an empty exposition. This is intentional
  (ADR-0014): disabled means absent.

## Continuation checkpoint for PRs 3.2 and 3.3

- The composition-root seam is established: later subsystems declare a narrow
  observer interface in their own package, keep it Prometheus-free, and let
  `app.Build` hand them the shared `*metrics.Metrics`. Extend the instrument
  set only with reviewed intent (ADR-0014).
- Remaining Tranche 3 outcomes (2–7) untouched by this PR; the surfaces the
  ADRs already name as next: dead-letter requeue/discard tooling and outbox
  retention policy (operator wave, ADR-0013), multi-node dispatcher
  scale-out, OpenTelemetry tracing/log correlation (the client_golang
  registry is a supported OTel scrape target), and alerting baselines over
  the series added here (dead-letter growth, dispatch failures, exhausted
  convergences, 5xx rate).
- Suggested next step for 3.2: pick one operator-wave outcome, extend
  `docs/TRANCHE_3_1_EVIDENCE.md`'s continuation list into its requirement
  matrix, and follow the same tranche discipline (seam first, deterministic
  tests, evidence record).
