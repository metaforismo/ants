# ADR 0017 — Redacted request logging, correlation semantics, and restart-convergence proof

Status: accepted
Date: 2026-08-23

## Context

The master plan requires structured, redacted, correlated logging (section
15.1) and telemetry/runbooks as part of every definition of done (sections
15.3, 21). ADR-0014 closed the metrics gap; two observability gaps remained,
named by the Tranche 3.3 handoff:

1. **Request logging leaks.** The existing API listener middleware logged the
   raw URL path — which embeds tenant-selectable resource identifiers
   (`/v1/threads/{id}`) — echoed an unbounded, unvalidated `X-Request-ID`
   header value straight into both the structured log and the response
   header, and wrapped the writer in a recorder that hides `http.Flusher`,
   `http.Hijacker`, and every other optional interface from handlers. Panic
   recovery logged the raw path too. None of this had a written contract.
2. **Restart durability is asserted, never proven.** At-least-once outbox
   delivery across process loss is the core operational promise of ADR-0011
   ("a crashed dispatcher's work is redelivered, never lost"), and the plan's
   failure-behavior table requires worker death to converge without double
   effects — but no automated test kills a real serving process mid-dispatch
   and proves convergence on PostgreSQL.

Scope guardrails carried from the tranche prompt: no OpenTelemetry, no remote
admin APIs, no multi-node leader election, no auth stubs, no paid services,
no new metric families unless reviewed as another ADR-0014 amendment.

## Decision

### One correlation vocabulary across requests, events, and operator actions

Every correlation identifier in Ants shares one grammar and one generation
primitive: `<prefix>_<suffix>` built by `domain.NewID`, suffixes being
26 alphanumeric characters. Events carry it as `trace_id` (plan section 11.3),
dead-letter CLI actions accept it as `--trace-id` (ADR-0015), and requests now
carry it as the documented `X-Request-ID` header.

Inbound header values are accepted verbatim only when they satisfy the
correlation acceptance grammar: 1–128 characters of `[A-Za-z0-9._~:@-]`. This
admits external systems' identifiers (UUIDs, trace ids from other stacks)
so cross-system correlation works, while refusing control characters,
whitespace, quotes, and oversized values — an attacker cannot inject garbage
into logs or response headers. Rejected or absent values cause a fresh
`req_…` identifier to be generated; `request` joins the ID prefix table so
the shape is contractual, not ad hoc.

**Truthfulness:** each request log states where its identifier came from —
`correlation_source="header"` or `correlation_source="generated"` — and the
response echoes exactly the identifier actually used, under the retained
`X-Request-ID` name (renaming would break the already-pinned response
contract for zero diagnostic gain).

Propagation of a request's correlation id into the `trace_id` of events the
request emits was deliberately **deferred** here: it requires context
plumbing through the orchestration engine and every store write path, which
is engine work, not middleware work. Update (2026-08-23, ADR-0018): that
deferral is now closed by the typed application-seam carrier — request-scoped
events and audit records carry the effective correlation id in their existing
`trace_id` slot; non-request writes keep empty ids.

### Request-log field contract (fixed, low-cardinality, redacted)

One line per served request at level Info/Warn/Error keyed to status class
(existing behavior). Exactly these fields:

| Field | Content | Bound |
|---|---|---|
| `method` | HTTP verb | fixed vocabulary |
| `route` | pinned mux pattern (`/v1/threads/{id}`, …) or `unmatched` | fixed vocabulary; identical to the metric route label source |
| `status` | final response code | closed set |
| `duration_ms` | handler duration in milliseconds | numeric |
| `request_id` | effective correlation identifier | ≤128 chars, validated above |
| `correlation_source` | `header` \| `generated` | fixed vocabulary |
| `remote_class` | `loopback` \| `private` \| `public` \| `unknown` | fixed vocabulary |

Never logged, structurally — not by omission but because the middleware has
no code path that could emit them: raw URLs and paths (so resource IDs never
reach logs), query strings, request/response bodies, `Authorization` and
`Cookie` headers (or any other header), tenant/principal/resource
identifiers, secrets, and client IP addresses. `remote_class` exists because
operators legitimately need to know whether traffic came from inside the box
without Ants retaining personal data; parsing failures classify as `unknown`
rather than guessing.

Panic records follow the same contract (`route`, `request_id`, bounded panic
value) at Error level. `/metrics` cardinality posture is untouched: the
logging layer registers no instruments and reuses the `unmatched` route
constant, so log labels and metric labels can never disagree about what a
route is.

### Writer fidelity: capability probing, not silent degradation

The status recorder must not change handler-visible semantics. It forwards
`Flush` and `Hijack` only when the underlying writer genuinely supports them,
by constructing one of a small set of concrete wrapper types after probing —
handlers type-asserting `http.Flusher` get a truthful yes/no, never a no-op
that silently swallows a flush. All wrappers expose `Unwrap()` so
`http.ResponseController` walks through to the real writer (streaming,
deadline control, connection switching keep working).

### Panic handling stays consistent

Recovery remains inside the request log so a recovered panic is observed with
its true outcome. One refinement: the RFC 9457 `internal` problem is written
only when the response has not started; a panic after headers (or mid-body)
cannot be retroactively converted to a 500, so the middleware logs the panic
and lets the truncated response stand — writing a second header would lie to
the client and to net/http. Metrics observe whatever status was actually
sent.

### Restart-convergence semantics (chaos-style integration test)

The property proven: **for any interleaving, after a serving epoch dies
mid-dispatch, a fresh epoch converges the durable outbox to fully delivered
with no duplicated logical effects at the consumer boundary.**

Semantics fixed before implementation:

- **Process death is real.** Epoch one is a separate OS process (helper
  execution of the test binary) holding its own store connections; it is
  killed with SIGKILL while dispatching. No graceful drain runs: in-memory
  state, leases, and pooled sockets die exactly as in a crash.
- **Attempts ≠ effects.** The test-only sink appends one row per *delivery
  attempt* (an attempt ledger) and separately applies the *logical effect*
  into an idempotent sink table keyed by event id with
  `ON CONFLICT DO NOTHING`. At-least-once shows up as attempts exceeding
  effects; exactly-once logical application shows up as
  `effects == seeded events`, enforced by the primary key even under
  concurrent redelivery.
- **Convergence criteria are state-based,** evaluated against the database:
  every outbox row `delivered`; effect count equals seeded event count; every
  effect's event id was seeded; total attempts strictly exceed total effects
  (redelivery genuinely happened). Waits poll these conditions under
  deadlines — timing never depends on fixed sleeps.
- **Bounded.** Configured intervals/leases/backoffs are production-legal
  minimums (dispatcher validation bounds), the whole scenario carries an
  explicit deadline, and scratch lives in the isolated per-test database plus
  repo-local ignored `.local/`.
- **The only substitution is the Sink.** The real PostgreSQL adapter, real
  transactional append, real dispatcher, and real lease/fencing machinery run
  unchanged. The production default `LogSink` cannot participate because it
  has no observable effects — proving "no duplicated logical effects"
  requires a consumer that *has* effects. The sink seam is precisely the
  extension point external subscribers will implement (ADR-0011); the test
  harness is the first such consumer, kept in `_test.go` files with zero
  production wiring, no debug endpoints, no config surface.
- **Broken-variant obligation:** the test's meaning is demonstrated by
  showing it fails against a meaningfully broken variant — the non-idempotent
  sink (effect insert without dedup key) reports duplicated logical effects
  under forced redelivery — recorded in the tranche evidence.

The manual end-to-end smoke complements this with the actual `ants serve`
binary and the production LogSink, where convergence (not effect identity) is
verifiable via outbox row states in SQL.

### Explicitly out of scope

OpenTelemetry tracing, log shipping configuration, alert *evaluation*
infrastructure (baselines are documentation over the closed metric set),
multi-instance dispatch, authenticated operator APIs, new Prometheus
instruments, new dependencies. Request-to-event trace_id propagation was
out of scope here and is now specified by ADR-0018.

## Alternatives considered

- **Keep logging raw paths** — rejected: resource identifiers are tenant data;
  logs are read by operators and shippers outside the tenant boundary.
- **Rename the header to `X-Trace-ID`** — rejected: breaks the pinned
  echo contract; the name is cosmetic, the grammar is what matters.
- **Simulate crash by cancelling contexts in-process** — rejected as the sole
  proof: graceful-shutdown paths would contaminate the crash semantics; leases
  held by a *living* process differ from orphaned ones. Used only as unit-level
  coverage elsewhere.
- **New counters (restarts, delivery lag, request body sizes)** — rejected:
  the closed ADR-0014 set already expresses everything alerting needs; the
  restart story is a tested property, not a gauge.
- **Client IP in logs with truncation** — rejected: still personal data;
  class conveys the operational signal without retention risk.

## Consequences

- Request logs become safe to ship: no identifiers, no secrets, no unbounded
  values, stable field names operators can index.
- Cross-system correlation works by construction: external trace ids flow in
  through `X-Request-ID`/`--trace-id` under one grammar, and generated ids are
  distinguishable from supplied ones.
- Streaming/hijacking handlers keep working behind the observability layer,
  pinned by tests, so future SSE/stream surfaces need no middleware rework.
- Restart convergence moves from documentation claim to executed proof on
  real PostgreSQL, with the harness isolated from production code.
- Deferred unchanged (named so they resurface): OTel export, alert evaluation
  tooling, multi-node dispatcher. Request→event trace_id propagation was
  closed by ADR-0018.
