# ADR 0014 — Prometheus metrics exposure on the API listener

Status: accepted
Date: 2026-08-23

## Context

ADR-0013 deferred "Prometheus/OpenTelemetry metrics exposure" to an
observability wave. The durable core now runs unattended subsystems whose
health is otherwise invisible without log scraping: the outbox dispatcher
(deliveries, retries, dead-letters, queue depth) and the run worker
(acquisitions, terminal outcomes, convergences), behind the HTTP edge
(request volume, latency, status mix). MASTER_PLAN sections 15.3 and 21 make
telemetry part of the definition of done and name Prometheus as the metrics
backend; the OSS atlas (section 14.6) lists the Prometheus Go client as
adopt.

## Decision

**Adopt `github.com/prometheus/client_golang`.** It is Apache-2.0, the
de-facto Go instrumentation standard, and satisfies the permissive-license
rule (ADR-0008). OpenTelemetry remains the later wave for distributed
tracing; a client_golang registry is a supported scrape target for OTel
collectors, so this choice does not foreclose that path.

**Composition-root-owned registry.** Metrics live in `internal/metrics`,
register on a fresh `prometheus.Registry` (never the global default), and
are wired explicitly by `app.Build` into the server, outbox dispatcher, and
run worker. Tests build or omit the collector freely; no package state leaks
between processes in one test binary.

**Same-listener `/metrics`, config-gated.** The endpoint serves on the
existing HTTP listener next to `/healthz` and `/readyz`, pinned by the same
route-table/OpenAPI drift gate under the `system` tag. `metrics.enabled`
(default true, env `ANTS_METRICS_ENABLED`) disables it entirely: disabled
means the route does not exist (uniform 404 problem), not a stub answer.
Enabling metrics requires a non-nil collector at construction; a server that
promises metrics must not silently serve none. A separate admin listener is
deliberately deferred until a deployment topology needs it.

**Bounded cardinality, no tenant data.** Labels are fixed vocabularies only:
HTTP series carry method, mux route pattern (never raw paths, so no IDs and
no cardinality blowup), and response status; outbox gauges carry message
state; worker counters carry terminal outcome and convergence kind. Tenant,
run, task, and principal identifiers are never labels. Aggregate operational
shape is considered safe to expose alongside health probes; deployments that
disagree can disable metrics until an ACL'd admin listener ships.

**Consumer-side observer interfaces.** `internal/outbox` and
`internal/worker` declare narrow observer interfaces and stay
Prometheus-free; `internal/metrics` implements them structurally. A nil
observer means instrumentation off: dispatch and execution behavior are
identical either way.

Initial instrument set (closed; extend only with reviewed intent):

- `ants_http_requests_total{method,route,status}`,
  `ants_http_request_duration_seconds{method,route}`,
  `ants_http_requests_in_flight`;
- `ants_outbox_messages{state}`, `ants_outbox_dispatch_rounds_total`,
  `ants_outbox_messages_leased_total`,
  `ants_outbox_messages_delivered_total`,
  `ants_outbox_messages_retry_scheduled_total`,
  `ants_outbox_messages_dead_lettered_total`;
- `ants_worker_claims_acquired_total`,
  `ants_worker_runs_finished_total{outcome}`,
  `ants_worker_runs_converged_total{kind}`;
- Go runtime and process collectors under the same registry.

## Consequences

- Every later subsystem (integrations, scheduler, sandbox drivers) adds its
  counters in the composition root seam pattern established here instead of
  retrofitting telemetry.
- Alerting baselines (dead-letter growth, dispatch round failures, exhausted
  convergences, 5xx rate) become expressible in the operations wave without
  further code.
- The exposition is unauthenticated by design like the health probes; if a
  future hardening review requires it, the endpoint moves behind the admin
  listener as one wiring change because the collector is injected, not
  global.
