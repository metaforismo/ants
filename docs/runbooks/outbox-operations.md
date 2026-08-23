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

Discard does not delete anything by itself: deletion belongs to retention.

## Retention sweeps (ADR-0016)

Terminal rows accumulate one-for-one with events: `delivered` rows are
delivery bookkeeping (the durable event carries all content), `discarded`
rows record operator triage. Retention deletes them in bounded rounds — and
only them:

- eligible: `delivered` older than `outbox.retention.delivered_after`;
  `discarded` older than `outbox.retention.discarded_after` (age measured
  from the terminal timestamp, inclusive at exactly the horizon);
- never eligible: pending, leased, dead rows; rows whose terminal timestamp
  is NULL; domain events; audit history. Dead letters stay forever until an
  operator discards them, and discarded rows survive every round until their
  horizon passes.

Defaults are inert: both horizons unset means nothing is ever deleted.
Configuring a horizon IS the opt-in. Each sweep deletes at most
`outbox.retention.batch_size` (default 500) rows, delivered victims first,
oldest-terminal-first within each class.

```sh
# What would a round collect right now? Never deletes:
ants outbox retention preview [--config config/ants.local.yaml] [--json]

# One bounded round; refuses without --yes and prints the same numbers:
ants outbox retention sweep --yes [--json]
```

Output contract matches the rest of the CLI: one stable line per result,
JSON object under `--json`, typed error triples on stderr, exit codes
0/1/2 where 2 includes an unconfirmed sweep. The unconfirmed refusal runs
the identical non-destructive selection first, so its message shows the real
counts it declined to delete.

When any horizon is configured, `serve` also runs sweeps automatically every
`outbox.retention.interval` (default 1h); the loop stops first during
graceful shutdown. Activity lands on
`ants_outbox_retention_deleted_total{state}` and
`ants_outbox_retention_rounds_total`.

Recovery note: sweeps are atomic per round. A crashed process leaves either
the whole round applied or none of it; rerunning converges to the same state
because deletion is idempotent on already-deleted rows. Deleted rows are
gone permanently — there is no undo — but the corresponding events remain in
`events` untouched, so no domain history is lost by reclaiming queue space.

## Alerting notes

Alert-ready baselines and PromQL over these series — including dead-letter
growth, retry pressure with well-formed denominators, and retention
stall detection — live in [alerting-baselines.md](alerting-baselines.md).
Quick references:

- `rate(ants_outbox_messages_dead_lettered_total[5m]) > 0` sustained, or
  `ants_outbox_messages{state="dead"}` growth;
- `ants_outbox_operator_actions_total{outcome!="succeeded"}` spikes
  (stale_credential bursts usually mean two operators or two automations are
  working the same queue) — triage signal, not a page.
