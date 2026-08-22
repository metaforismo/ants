# ADR 0011 — Transactional outbox with store-owned clock

Status: accepted
Date: 2026-08-22

## Context

ADR-0005 deferred the durable transactional outbox. Since then the unit-of-work
seam (ADR-0010) made it possible to persist a state change and its event in one
commit; what remained missing is durable *delivery*: consumers today poll the
API by cursor, so a subscriber that is down misses notifications, and there is
no retry story for failed delivery. The plan requires the outbox entry in the
same DB commit as the transition (section 6.3).

Two prior attempts also surfaced a design trap: scheduling decisions (when a
message becomes publish-visible, when a lease expires, when a retry is due)
were drifting between adapters and leaking timestamps through public port
signatures (`AvailableAt` / `AvailableBefore` parameters), which made
deterministic testing impossible without sleeps and let callers influence
store behavior.

## Decision

**Transactional enqueue.** `EventRepository.Append` writes the event and its
outbox message atomically:

- PostgreSQL: inside `Store.Do` (ADR-0010) — joins the caller's transaction or
  creates one; rollback removes both rows.
- Memory: under the same write lock, and covered by whole-state snapshot
  restore for units, so the identical rollback contract holds.

The dedup key derives from the event ID (`event:<id>`), generated once at
publish time, so at-least-once redelivery stays consumer-deduplicable.
Publishing is idempotent on that key.

**Store-owned injected clock.** Each adapter owns exactly one `ports.Clock`
(`SystemClock` by default) as the single time authority. The ports expose only
durations and never timestamps: `OutboxLeaseRequest.LeaseFor`,
`FailWithBackoff(retryIn)`. Due-ness, lease expiry, and retry instants are all
evaluated against the store's clock. There is no `AvailableAt` parameter in
any public signature. Tests inject an advancing manual clock
(`storetest.AdvancingClock`) shared with the adapter, making every timing
behavior deterministic without sleeping.

**Delivery semantics.** At-least-once, with exclusive leases:
`Lease` claims due messages (PostgreSQL via `FOR UPDATE SKIP LOCKED`) and
counts the attempt at claim time; ack requires the active lease; failure
reschedules with caller-computed exponential backoff capped to avoid duration
overflow; exhausting `max_attempts` dead-letters the message terminally.
Expired leases make messages reclaimable — a crashed dispatcher's work is
redelivered, never lost.

**Dispatcher.** An in-process worker (`internal/outbox`) drains batches on a
configurable interval, hands envelopes to a `Sink`, and records outcomes. The
production default sink logs deliveries until external subscribers exist;
wiring lives in the composition root and starts/stops with `serve`.
Configuration (`outbox.*`: batch size, interval, lease, max attempts, backoff
base) is typed and validated at startup like every other section.

Both adapters must satisfy the identical behavioral contract
(`storetest.RunOutbox`): idempotent publish, exclusive concurrent leases,
lease-gated acks, backoff then dead-letter, expired-lease reclaim, stats over
all states, and the dual-write atomicity properties (committed event ⇒ exactly
one queued delivery; rolled-back unit ⇒ neither row).

## Consequences

- Consumers must deduplicate on event ID; redelivery after a crash is normal,
  not exceptional.
- Dead-lettered messages are terminal and observable via `Stats`; operator
  tooling (requeue/discard) is future work — nothing silently retries forever
  or drops.
- The dispatcher is single-process; horizontal scale-out replaces the driver,
  not the port — `ports.OutboxStore` already encodes lease exclusivity.
- Because all scheduling reads flow through the injected clock, no test in the
  suite sleeps for outbox behavior, and both adapters remain behaviorally
  pinned to the same contract.
