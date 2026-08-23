# ADR 0018 — Request correlation propagation into events and audit records

Status: accepted
Date: 2026-08-23

## Context

ADR-0017 unified the correlation vocabulary (one grammar, one generation
primitive) across request logs (`X-Request-ID`), event `trace_id` slots
(plan section 11.3), and operator `--trace-id` provenance (ADR-0015) — but
the vocabularies only aligned, they did not flow. A request's effective
correlation id died in the log line: the events persisted while serving that
request carried empty `trace_id`, so an operator could not join a response,
its request log, the resulting event history, or an audit record into one
trace. ADR-0017 named this propagation as deliberately deferred because it
is engine work: it requires context plumbing through the orchestration seam,
not another middleware tweak. This ADR closes exactly that deferral.

The master plan makes trace ids part of every transition's required metadata
(section 6.3) and of the event envelope (section 11.3); sections 15.1 and
15.3 require correlated, redacted observability as definition-of-done.

## Decision

### One typed carrier at the application seam

A new leaf package, `internal/correlation`, owns everything about correlation
identity that more than one layer needs — nothing else:

```go
type ID string            // validated at the trust boundary, opaque after
const MaxLength = 128     // shared acceptance bound (ADR-0017 grammar)
func Valid(s string) bool // THE grammar; no other copy may exist
func With(ctx context.Context, id ID) context.Context
func From(ctx context.Context) (ID, bool)
func TraceID(ctx context.Context, explicit string) string
```

`With`/`From` carry the identifier inside the standard `context.Context`
using an unexported key — the same pattern the unit of work uses for its
transaction (ADR-0010). There is no global mutable state, no ambient
package variable, no stringly-typed context key, and no handler-signature
change: the middleware wraps the request context once, and every emission
seam downstream already receives that context.

`TraceID(ctx, explicit)` encodes the precedence rule in one place:
an explicitly set value always wins; otherwise a request-scoped context
contributes its carried identifier; otherwise the field stays empty.

### Trust boundary and propagation semantics

- **Validation happens once, at the edge.** The HTTP middleware keeps sole
  responsibility for resolving the effective id (accept well-formed inbound
  `X-Request-ID` verbatim; generate `req_…` otherwise; entropy failure
  handling unchanged from ADR-0017) and is the ONLY producer of the carrier.
  Everything downstream consumes an already-validated identifier.
- **Set after resolution, before dispatch.** The middleware inserts the
  carrier immediately after resolution, so auth denial paths, malformed-body
  failures, panics, and happy paths all observe the same identity — the one
  echoed in the response header and written to the request log.
- **Emission seams fill the slot.** Exactly three sites persist records with
  a `trace_id` field, and each fills it through `correlation.TraceID`:
  `orchestration.Engine.emitEvent` (every engine-emitted event),
  the server's tenant-creation event emitter, and the policy engine's audit
  appender. Records keep their existing columns and JSON shapes — this is
  plumbing into the existing `trace_id` slot, not a schema change.
- **Explicit beats ambient.** The dead-letter operator service continues to
  take its provenance exclusively from the CLI's explicit `--trace-id`;
  ambient carriers never overwrite an operator-declared value. For symmetry
  and drift-prevention, `OutboxMutationRequest.Validate` now rejects
  non-empty trace ids that fail the shared grammar — previously arbitrary
  bytes could land in durable audit payloads. Grammar-compatible values
  (including every previously documented example) are unaffected.
- **No fabricated identities off the request path.** Worker execution,
  outbox dispatch, retention sweeps, the demo pipeline, and plain CLI
  invocations run without a carrier: their events keep empty `trace_id`,
  exactly as today. A run started over HTTP is executed later by the worker;
  the execution-phase events belong to that worker epoch, not to the long-
  finished HTTP request, and must not inherit its identity. Cancellation
  paths behave the same: terminal writes made outside any request stay
  unattributed to one.

### Audit/event consistency

Event `trace_id` and audit `trace_id` draw from the same carrier, the same
grammar, and the same precedence rule, so a single identifier joins an HTTP
response, its request-log record, every event committed while serving it,
and any audit record written in that window — provable by equality tests.
Where a durable record already exists on denied/failure paths (policy
denials during execution write denied-result audits), those records follow
the same rule: they carry the carrier of the context that produced them and
nothing else. No new audit records are introduced for auth denials; the API
problem response plus the request log remain the denial trail.

### Transaction behavior

Correlation affects only the content of a field on records that already
commit atomically with their state changes (ADR-0010/0011). A rolled-back
unit leaves no orphan rows and no half-correlated history; a committed unit
carries the correlation on every record it produced. Nothing about timing,
retry, or redelivery semantics changes: envelopes are serialized once at
publish time, so a redelivered delivery replays the same `trace_id` it was
published with — process death cannot rewrite correlation history.

### Non-goals

OpenTelemetry spans or exporters; span trees; W3C traceparent parsing;
correlation between separate requests (fan-out attribution); new metrics,
dependencies, migrations, or OpenAPI surface; retroactive rewriting of
persisted envelopes; multi-node dispatcher concerns (unchanged, ADR-0011/
0013).

## Alternatives considered

- **Command-metadata struct threaded through every engine call** — rejected:
  signature churn across the whole orchestration surface for data the
  context already carries; ADR-0010 rejected the same trade-off for
  executors.
- **Carrier in `internal/domain`** — rejected: domain stays free of context
  plumbing; the carrier is application transport, not domain vocabulary.
  The grammar remains shared by living in the one leaf package both sides
  import.
- **Store-layer extraction from context inside adapters** — rejected: stores
  would silently invent provenance for callers (tests, CLI, future services)
  that never opted in; filling the slot belongs to the emission seams that
  know what the field means.
- **Propagate the request id into worker-executed run events via durable
  run metadata** — rejected for this tranche: it fabricates an HTTP identity
  for work performed by a different principal (the worker) at a different
  time, which is exactly the dishonesty the truthfulness rule forbids. If
  cross-time attribution is ever needed it must be modeled explicitly (e.g.
  a `requested_by_trace` field), not smuggled through `trace_id`.
- **Validate operator `--trace-id` only in the CLI flag parser** — rejected:
  validation at the port boundary protects every future caller of the
  service seam, not just today's CLI, and avoids duplicating the grammar in
  the command layer.

## Consequences

- Response header == request log id == persisted event `trace_id` == related
  audit correlation for every HTTP-triggered command, pinned by integration
  tests including concurrency, failure rollback, and auth-denial paths.
- Operators can finally answer "which request caused this event?" with one
  equality join instead of timestamp guesswork; the alerting runbook gains a
  concrete investigation path.
- External systems' identifiers flow end-to-end verbatim under the ADR-0017
  acceptance grammar; generated ids remain distinguishable by prefix.
- Malformed operator trace ids are now typed rejections instead of silent
  garbage in audit history — an intentional behavior change for inputs that
  were never valid.
- Deferred unchanged: OTel export, alert evaluation tooling, multi-node
  dispatch, authenticated remote operator APIs, cross-request fan-out
  attribution.
