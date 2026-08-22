# ADR 0013 — Operational hardening: bounded dispatch, honest readiness, production auth gate

Status: accepted
Date: 2026-08-22

## Context

Tranche 2 made execution durable (ADR-0010/0011/0012): state transitions,
events, outbox deliveries, run claims, and a process-level worker. What
remained between that stack and an operable deployment were four documented
gaps, each with a bounded fix:

1. Reclaim is unbounded. A claim whose executions keep ending without a
   terminal state — engine aborts before the first transition, lost leases,
   shutdown releases — is reclaimed forever; `attempts` only counts.
   D2 explicitly deferred retry caps to "engine policy for a later tranche".
2. Readiness guesses. `/readyz` probed persistence by reading a sentinel
   tenant slug (`__readiness_probe__`), which exercises one query but cannot
   distinguish pool health from query success, reports nothing about how the
   check is bounded, and hides the dependency seam inside the server package.
3. Dev auth posture. ADR-0004 required removing dev-header authentication
   from production profiles before any SaaS beta; nothing enforced it.
4. HTTP server limits. The listener set read/write timeouts but no idle
   timeout (keep-alive sockets held forever) and no explicit header bound.

## Decision

**Bounded dispatch per claim (run-level dead-letter).** `worker.max_attempts`
(default 3, validated [1,10], env `ANTS_WORKER_MAX_ATTEMPTS`) caps how many
times a claim may be acquired. An acquisition that lands within the budget
executes; one beyond it converges the run instead of executing:

- a run still **pending** never started, so it converges to `cancelled` —
  the closed run machine has no pending→failed edge, and cancellation is the
  honest disposition for work that never began;
- a run abandoned **mid-flight** converges to `failed` with code
  `run_attempts_exhausted`, mirroring recovery's classified convergence.

Both paths commit state + status event in one unit of work (event data
carries `code` and `attempts`), route the thread like their outcome does
(failed → thread failed / needs_attention; cancelled → awaiting_input /
needs_attention), are idempotent for terminal runs, and deliberately touch
no claim row: deletion stays with the credential-holding epoch via guarded
operations (ADR-0012). A failed convergence leaves the claim to expire and
retry — no half-converged state.

Every acquisition counts toward the budget, including epochs ended by
shutdown or lease loss, because attempts counts acquisitions by contract
(ADR-0012). Operators sizing the bound must account for deploy churn as well
as real faults; this trade-off was chosen over inventing a second,
execution-quality-aware counter on the claim row.

**Honest readiness via injected dependency checks.** The server requires a
readiness function at construction — nil wiring is a startup error, not a
silent always-ready fallback. The composition root supplies it per store
mode: PostgreSQL gets a pool `PingContext`; memory has no external
dependency and gets a trivially satisfied check. Each probe runs under
`server.readiness_timeout` (default 2s, env `ANTS_SERVER_READINESS_TIMEOUT`)
so a slow database fails fast instead of hanging health checks. Failure is a
transient RFC 9457 problem (`store_unavailable`, 503); liveness stays
independent of dependency state. The sentinel-slug probe is deleted.

**Dev auth confined to loopback at startup.** With `dev_header_auth: true`,
configuration validation refuses any bind address whose host is not a
literal loopback IP or `localhost`. Wildcard and LAN binds fail startup with
a typed message instead of exposing unauthenticated tenant switching to the
network; disabling dev auth keeps every bind address legal. This implements
the enforcement half of ADR-0004 without removing the development mode.

**HTTP listener bounds.** The listener gains `server.idle_timeout`
(default 120s, env `ANTS_SERVER_IDLE_TIMEOUT`, validated positive) so idle
keep-alive connections are reclaimed, and sets `MaxHeaderBytes` to the
stdlib default explicitly so header size is a stated property of the
deployment, not an accident. Strict JSON decoding now passes the real
`http.ResponseWriter` into `http.MaxBytesReader` so oversized bodies also
flag the connection for teardown.

## Consequences

- Poison runs converge visibly with their attempt count on the event stream
  instead of consuming reclaim capacity forever; the last unbounded retry
  loop in the execution path is closed, matching the outbox's max-attempts
  semantics.
- Readiness reflects actual backing dependencies through one injected seam;
  future stores or services add a check in the composition root, not inside
  HTTP handlers.
- The production auth gate turns a documentation rule into a startup
  failure; OIDC remains the real replacement (Horizon 1 item).
- Deferred unchanged: Prometheus/OpenTelemetry metrics exposure (observability
  wave), dead-letter requeue/discard tooling and outbox retention policy
  (operator wave), multi-node dispatcher scale-out.
