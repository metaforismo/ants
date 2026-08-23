# ADR 0015 — Outbox dead-letter operator tooling: inspect, requeue, discard

Status: accepted
Date: 2026-08-23

## Context

ADR-0011 left dead-lettered outbox messages "terminal and observable via
`Stats`" with operator requeue/discard explicitly deferred to an operator
wave (named again by ADR-0013). Tranche 3.1 added the metrics platform whose
dead-letter series make queue poisoning visible but not actionable: once a
message exhausts its delivery budget it stays dead forever, the only remedy
is direct database surgery, and nothing records who intervened or why. This
closes that gap for exactly one outcome of Tranche 3 and defers everything
else.

## Decision

### State machine, declared where all machines live

The delivery lifecycle becomes an explicit domain transition table alongside
thread/task/run (AGENTS.md invariant; enumerated by the same consistency
test). One state is added:

```text
pending → leased                    (dispatcher claim)
leased  → delivered                 (ack)
leased  → pending                   (classified backoff retry)
leased  → dead                      (attempts exhausted)
dead    → pending                   (operator requeue)   [new]
dead    → discarded                 (operator discard)   [new]
delivered, discarded                (terminal, no edges)
```

Requeue and discard are valid only from `dead`. A discarded message is never
deletable by this feature: history is retained in place and retention/GC
remains deferred. Requeue resets exactly what a fresh bounded delivery
lifecycle needs — attempts to zero, availability to the store clock's present
instant, lease fields cleared — and preserves immutable identity and
provenance: id, dedup key, tenant, envelope, max_attempts, last_error (until
the next failure overwrites it). The fresh lifecycle is bounded by the same
max_attempts budget, so repeated operator requeues never create unbounded
automatic retries; each manual intervention is itself audited.

### Concurrency credential: monotonic generation, compare-and-swap

Every row carries a monotonically increasing `generation`. It increments on
each transition into `dead`, `pending`(from dead), or `discarded` — precisely
the transitions operators can act on. A mutation request must carry the
generation read from a prior list/show; the store applies the mutation only if
status is still `dead` AND generation matches. Mismatches are typed conflicts
(`stale_version`), wrong-state is `invalid_transition`, unknown or
foreign-tenant messages are uniform not-found (ADR-0004 anti-enumeration).
There is deliberately no last-write-wins path: a stale operator cannot
overwrite a newer retry/delivery epoch. Repeats are therefore detectable, not
silent: replaying a lost response surfaces a conflict naming the newer
generation instead of pretending success.

### Authorization/audit boundary: local operator CLI, no HTTP route

The product surface is the CLI over the typed store/application seam
(`app.Build`), not the HTTP API. Remote operator auth does not exist yet:
OIDC principals (Horizon 1 item) would force either an unauthenticated
mutation route — forbidden — or fake authorization theater around
self-declared headers. Until authenticated operator principals exist, the
boundary is honest: whoever can run the CLI has store privileges, the same
trust level as `migrate up`, and every mutation says who ran it. The API
surface gains nothing this tranche; OpenAPI and generated contracts are
untouched. When operator APIs arrive they must require real authn/tenant
principals and reuse this same application seam.

Every successful requeue/discard commits three records in ONE unit of work:
the guarded row mutation, a versioned domain event
(`outbox.dead_letter.{requeued,discarded}.v1`, aggregate
`outbox_message`, aggregate_version = post-op generation) whose durable
outbox delivery enqueues atomically via the existing append path (ADR-0011),
and a tenant-scoped audit record (actor, action, target, outcome, trace id,
bounded reason metadata; no secrets, no envelope bytes). Any failure rolls the
unit back entirely — a requeue without its audit trail cannot exist. Operator
actions are declared in the policy action vocabulary but structurally denied
by the engine's default rule: agent execution paths must never reach this
tooling, and wiring real evaluation waits for real principals.

### Listing: deterministic, bounded, envelope-free

Dead letters paginate in `(created_at, id)` order behind an opaque cursor
token; page size is validated to [1,200]. Summaries carry identity, counts,
generations, timestamps, and the stored bounded failure cause (same 512-char
bound the dispatcher writes) — never raw envelope bytes, which stay in the
store. Sink error strings follow the same redaction contract as dispatcher
logs; listing surfaces them because an operator cannot triage a dead letter
without knowing why it died.

### Metrics: reviewed amendment to the ADR-0014 closed set

One counter family joins via the established consumer-side observer seam:
`ants_outbox_operator_actions_total{action,outcome}` with action ∈
{requeue, discard} and outcome ∈ {succeeded, stale_credential,
invalid_state, not_found, invalid_request, failed}. Labels are fixed
vocabularies; message, tenant, and principal identifiers are never labels.
The existing `ants_outbox_messages` gauge samples the new `discarded` state.
Observers run
inline after persistence outcomes are known and cannot alter behavior.

### CLI posture

`ants outbox dead-letter list|show|requeue|discard`, each taking an explicit
`--tenant`. Mutations require `--actor` (recorded verbatim) and accept
optional `--reason` and `--trace-id` for provenance. Discard is destructive
and gated by an explicit `--yes` flag; without it the command prints the
target and exits with usage status — there is no interactive prompt, so
automation and tests can never hang. Output is one stable line per result or
JSON lines under `--json`; failures print `error: <kind>: <code>: <message>`
on stderr with non-zero exit codes keyed to usage vs operational failure.

## Alternatives considered

- **HTTP operator API now** — rejected: no real operator authn exists; an
  unauthenticated mutation endpoint violates the standing security posture.
- **Delete-on-discard** — rejected: silently destroying the only forensic
  record of a poison message hides exactly the incident it exists to explain;
  retention policy is the reviewed place for deletion.
- **Reuse `attempts` as the fencing token** — rejected: after a requeue and a
  second death the attempt count repeats, letting a credential from the first
  death act on the second. Only a monotonic generation is safe.
- **Batch requeue/discard** — deferred: single-message semantics are the
  primitive batches need; bulk selection UX is product work, not plumbing.

## Consequences

- Poison deliveries become operable without database access, and every
  intervention lands on the event stream and audit log with its actor.
- Stale-operator races converge on typed conflicts rather than lost updates;
  the stress contract pins one-winner behavior under concurrent requeue,
  discard, and dispatcher claims.
- Both adapters satisfy identical semantics through the shared store contract
  suite; a forward-only migration adds the generation/timestamp columns and
  extends the status check.
- Deferred unchanged: outbox retention/GC, multi-node dispatcher scale-out,
  OpenTelemetry tracing, batch operator operations, authenticated remote
  operator APIs, alerting baselines over the new series.
