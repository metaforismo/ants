# Runbook — Outbox dead-letter operations

Applies to the durable outbox delivered by ADR-0011 and operated through
the tooling added in ADR-0015. Read this before touching a poisoned
delivery.

## What a dead letter is

Every domain event commits with one durable outbox delivery (same
transaction, ADR-0011). An in-process dispatcher leases bounded batches,
hands them to the configured sink, and reschedules failures with
exponential backoff. After `outbox.max_attempts` (default 5) failed
attempts the message becomes `dead`: it is never claimed again, it stays
in the database, and its last failure reason is kept on the row.

Dead letters are visible in metrics as `ants_outbox_messages{state="dead"}`
(sampling lag of up to one dispatch interval applies) and
`ants_outbox_messages_dead_lettered_total`.

## Inspecting

```sh
ants outbox dead-letter list --tenant ten_xxx [--limit 50] [--after TOKEN]
ants outbox dead-letter show  --tenant ten_xxx obx_evt_yyy
```

- `list` prints one row per line in deterministic `(created_at, id)` order;
  the trailing `-- next page: --after TOKEN` hint feeds the next call.
  `--json` emits one JSON object per line instead.
- `show` renders full detail (dedup key, generation, attempt budget,
  timestamps, bounded cause) for dead **or discarded** rows; anything else
  is uniformly not-found.
- The `cause` field is the stored sink error, truncated to 512 characters.
  It never contains envelope payloads; sink implementations must keep error
  strings secret-free like all other diagnostics.

A requeued message disappears from these views until (unless) it dies
again — that is expected, not data loss.

## Deciding: requeue or discard

| Signal | Typical action |
| --- | --- |
| Cause shows a transient outage (sink/database/network) that has since ended | requeue |
| Message predates a deploy that fixed the failing consumer | requeue |
| Payload is obsolete, duplicated, or provably unprocessable | discard |
| Cause unclear after reading events around the dedup key | do nothing; investigate first |

Both actions require `--tenant` and `--actor`; both append a versioned
domain event (`outbox.dead_letter.{requeued,discarded}.v1`), enqueue its
durable delivery, and write a tenant-scoped audit record inside ONE
database transaction. There is no way to intervene without leaving this
trail, and no way for a half-applied intervention to commit.

```sh
# Restart a fresh bounded lifecycle (attempts reset, immediately due):
ants outbox dead-letter requeue --tenant ten_xxx --actor alice@example \
  obx_evt_yyy --reason "sink outage resolved"

# Terminal decision — requires --yes, retains the row as history:
ants outbox dead-letter discard --tenant ten_xxx --actor alice@example \
  obx_evt_zzz --yes --reason "obsolete duplicate of obx_evt_www"
```

Without `--yes`, discard prints exactly what it would do and exits with
usage status 2. The command never prompts interactively, so scripts and
automation cannot hang on it.

## Concurrency semantics

Mutations are compare-and-swap guarded by a per-row monotonic
`generation`. Every death, requeue, or discard bumps it. If someone else
(or another automation) acted on the message first, you get:

```text
error: conflict: stale_version: outbox_message was modified concurrently
```

Re-read with `show` and decide again; your earlier intent was NOT applied.
Wrong-state rows answer `invalid_transition`; unknown and foreign-tenant
ids are indistinguishable not-found by design (anti-enumeration).

Exit codes: `0` success, `1` operational failure (typed error triple on
stderr), `2` usage problems including an unconfirmed discard.

Error triples are stable: `error: <kind>: <code>: <message>`.

## Bounded retries, bounded interventions

Requeue resets attempts to zero but keeps the original
`max_attempts` bound, so an automatically failing delivery dead-letters
again on its own. Requeueing repeatedly against a permanently broken
consumer just produces audited churn — fix the sink first, then requeue.
There is no automatic path from dead back to pending; every restart names
an operator.

Discard does not delete anything: retention/GC for terminal rows is a
separate deferred policy (ADR-0015).

## Alerting notes

Baseline candidates over series from ADR-0014/0015:
`rate(ants_outbox_messages_dead_lettered_total[...]) > 0 sustained`,
`ants_outbox_messages{state="dead"} growth`, and
`ants_outbox_operator_actions_total{outcome!="succeeded"}` spikes
(stale_credential bursts usually mean two operators or two automations are
working the same queue).
