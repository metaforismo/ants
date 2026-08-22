# ADR 0005 — Events, idempotency, and the deferred outbox

Status: accepted
Date: 2026-08-22

## Context

The plan's event contract (section 11.3) requires immutable, versioned event
envelopes with actor, trace, aggregate version, and no PII/secrets; task
transitions require an outbox event in the same DB commit (section 6.3).

## Decision

Tranche 1 implements:

- The full envelope shape (`domain.Event`) with versioned type names
  (`task.status.changed.v1`); new shapes require new `.vN` suffixes.
- A per-store monotonic cursor (`seq`) with stable `after=` pagination on the
  API, so UI reconnects never duplicate or skip.
- Idempotency keys unique per (tenant, thread) at the store layer; duplicate
  starts replay the original run — including the concurrent-creation race,
  which resolves to the winner's run.
- Optimistic concurrency (`expectedVersion`) on every mutating store method.

Deferred explicitly: a durable transactional outbox. The memory store emits
events under the same lock that commits state, so the slice is consistent;
the Postgres adapter will write events in the same transaction as state
(outbox table + relay) when it lands. Until then the durable-mode gap is a
documented limitation, not a silent one.

## Consequences

- Event consumers today are pull-based (API polling by cursor), which the
  current product surface needs anyway.
- Adding the outbox later changes adapter internals, not the envelope or the
  domain code that emits events.
