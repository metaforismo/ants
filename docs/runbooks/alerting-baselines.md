# Runbook — Alerting baselines over the closed metric set

Alert-ready starting points for every series Ants actually exposes
(ADR-0014 closed set, including the ADR-0015 operator and ADR-0016 retention
amendments). No expression here requires a new instrument; if an alert seems
to need one, that is an ADR-0014 amendment first, code second.

**These thresholds are starting points, not guarantees.** They encode
assumptions about a small single-node deployment (one `serve` process, one
dispatcher, default configuration). Every deployment must calibrate against
its own baseline traffic before paging on them; a threshold that fires on
your Tuesday-morning deploy churn is worse than none.

Conventions: `job="ants"` selects the API/serve process scrape target.
Windows of 5m suit fast signals; 1h windows suit slow accumulations.
Ratios over low traffic are unstable — see the denominator caveats per
section.

## 0. Investigating with correlation ids

Metrics say *that* something happened; correlation ids say *which request*.
Every served request resolves one identifier (ADR-0017/0018) that appears in
four joinable places: the `X-Request-ID` response header, the request log's
`request_id` field, the `trace_id` of every event committed while serving
the request, and any audit record written in that window. Operator actions
join through their explicit `--trace-id` instead. Work performed outside a
request (worker execution, dispatch, retention) intentionally keeps empty
trace ids — an empty slot means "not request-caused", not "lost".

Investigation path for a 5xx burst, dead-letter spike, or odd event:
grab the id from one surface (log line or client-reported header) and query
the others:

```sql
SELECT type, aggregate_type, aggregate_id, occurred_at
  FROM events WHERE trace_id = '<id>';
SELECT action, resource_type, result, at
  FROM audit_events WHERE trace_id = '<id>';
```

## 1. Dead-letter growth (page)

A dead letter is a message that exhausted its delivery budget; it never
retries again without an operator (see [outbox-operations.md](outbox-operations.md)).

```promql
# Any dead-lettering at all is worth seeing immediately:
increase(ants_outbox_messages_dead_lettered_total[5m]) > 0
```

```promql
# Queue-depth form — catches deaths that predate the current process and
# confirms what the counter implies:
ants_outbox_messages{state="dead"} > 0
```

Caveats: the gauge samples once per dispatch round (up to one interval of
lag); `increase()` over short windows under-counts resets caused by process
restarts. Alert on the counter for speed, confirm with the gauge, then work
the queue with `ants outbox dead-letter list`.

## 2. Dispatch failures / retry pressure (warn)

Retries are normal in transit; sustained retries mean the sink or database
is unhappy.

```promql
# Retry scheduling rate:
rate(ants_outbox_messages_retry_scheduled_total[5m])

# Retry share of terminal delivery outcomes. Denominator = delivered +
# retried + dead-lettered in the window; all three are counters of the same
# dispatch loop, so the ratio is well-formed even at low volume:
sum(rate(ants_outbox_messages_retry_scheduled_total[5m]))
/
sum(rate(ants_outbox_messages_delivered_total[5m])
  + rate(ants_outbox_messages_retry_scheduled_total[5m])
  + rate(ants_outbox_messages_dead_lettered_total[5m])) > 0.1
```

Suggested start: warn when share exceeds 10% for 15m; page when it exceeds
50% for 10m **and** section 1 has not fired.

Caveat: when the loop goes completely idle the denominators approach zero
and Prometheus produces no samples rather than NaN spikes — absence of
samples is not health. Pair with the stall check below.

```promql
# Dispatch liveness — rounds must keep happening whenever anything is queued:
rate(ants_outbox_dispatch_rounds_total[5m]) == 0
and on() (ants_outbox_messages{state="pending"} > 0)
```

## 3. Retention activity and stall (warn only when configured)

Retention is inert unless horizons are configured (ADR-0016). Only alert a
deployment that deliberately enabled it:

```promql
# Rounds stopped while retention is expected to run:
increase(ants_outbox_retention_rounds_total[2h]) == 0
and on() (max_over_time(ants_outbox_retention_rounds_total[24h]) > 0)
```

The right-hand guard distinguishes "configured but stalled" from "never
enabled": a process that has ever completed a round had retention active.
After a restart the counter resets; give the first post-restart round one
interval (`outbox.retention.interval`, default 1h) before paging.

```promql
# Reclaim throughput, by state:
sum by (state) (rate(ants_outbox_retention_deleted_total[1h]))
```

Caveat: zero deletion rate alone is healthy — it usually means nothing aged
past its horizon. Stall detection needs the rounds counter, not deletions.

## 4. API 5xx rate (page)

```promql
# Error ratio with traffic-scaled burn-rate shape:
sum(rate(ants_http_requests_total{status=~"5.."}[5m]))
/
sum(rate(ants_http_requests_total[5m])) > 0.02
```

Denominator caveat: below roughly ten requests per second the ratio swings
violently on single failures. For quiet deployments alert on absolute error
rate instead:

```promql
sum(rate(ants_http_requests_total{status=~"5.."}[5m])) > 0.05
```

Suggested start: page on ratio > 2% for 10m under meaningful traffic, or on
absolute rate above one error every 20s when quiet. Recovered panics are
included here by design (they answer 500).

## 5. Readiness (page)

`/readyz` failure is not a metric — it is a probe result. Wire a blackbox
check (HTTP GET returning non-200 means the store ping failed with
`store_unavailable`) and alert on probe failure for 1m. Liveness (`/healthz`)
stays independent by design; if liveness fails, your orchestrator restarts
and this runbook is not the tool.

Restart convergence context: a serve process that dies mid-dispatch resumes
delivery after restart (proven by the restart-convergence integration test,
ADR-0017) — so a readiness blip during redeploy does not require operator
action unless dead letters appear afterwards.

## 6. Worker signals (warn)

```promql
# A run whose dispatch budget was exhausted converged as failed/cancelled
# instead of executing — poison or chronic churn:
increase(ants_worker_runs_converged_total{kind="exhausted"}[30m]) > 0

# Runs abandoned mid-flight and classified-failed by a later epoch:
increase(ants_worker_runs_converged_total{kind="interrupted"}[30m]) > 0
```

`exhausted` counts include legitimate deploy churn (every acquisition burns
budget — ADR-0013), so size `worker.max_attempts` before treating this as a
fault signal. There is deliberately no claim-queue depth gauge; if runs sit
unclaimed, `worker_claims_acquired_total` stalls while outbox events from
run creation continue — detect starvation as:

```promql
rate(ants_worker_claims_acquired_total[10m]) == 0
and on() increase(ants_http_requests_total{route="/v1/threads/{id}/runs",status="202"}[10m]) > 0
```

That expression couples two unrelated subsystems and is intentionally
conservative; investigate manually before paging on it.

## 7. Saturation (context, not paging)

```promql
ants_http_requests_in_flight
```

Useful as a dashboard panel and for capacity calibration. A sustained climb
with flat request rate suggests downstream slowness (usually the store);
cross-read section 2's retry share.

## Series inventory (everything this runbook may reference)

| Series | Type | Labels |
|---|---|---|
| `ants_http_requests_total` | counter | method, route, status |
| `ants_http_request_duration_seconds` | histogram | method, route |
| `ants_http_requests_in_flight` | gauge | — |
| `ants_outbox_messages` | gauge | state |
| `ants_outbox_dispatch_rounds_total` | counter | — |
| `ants_outbox_messages_leased_total` | counter | — |
| `ants_outbox_messages_delivered_total` | counter | — |
| `ants_outbox_messages_retry_scheduled_total` | counter | — |
| `ants_outbox_messages_dead_lettered_total` | counter | — |
| `ants_outbox_operator_actions_total` | counter | action, outcome |
| `ants_outbox_retention_deleted_total` | counter | state |
| `ants_outbox_retention_rounds_total` | counter | — |
| `ants_worker_claims_acquired_total` | counter | — |
| `ants_worker_runs_finished_total` | counter | outcome |
| `ants_worker_runs_converged_total` | counter | kind |

Plus the Go runtime and process collectors on the same registry. Anything
else you find in `/metrics` belongs to client_golang itself; anything missing
means the ADR-0014 set changed — update this inventory in the same change.

Operator actions (`ants_outbox_operator_actions_total{outcome!="succeeded"}`)
are triage signals, not alerts: bursts of `stale_credential` mean two actors
are working the same queue (see outbox-operations.md).
