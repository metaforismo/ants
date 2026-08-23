# Ants

Ants is an open-source, durable software-engineering agent platform built around planning, isolated execution, recursive collaboration, evidence, and human control.

Like an ant colony, specialized agents divide work, operate independently, share durable facts, and combine their results into a verified outcome.

## Status

Two tranches are implemented and green. The deterministic vertical slice runs
end to end — request → plan/spec → isolated parallel tasks → integration →
tests → evidence-based report — with real git commits and really executed
verification commands. On top of it, execution is durable: state transitions,
events, and their deliveries commit atomically (unit of work + transactional
outbox), runs are dispatched through tenant-scoped run claims with fencing and
bounded retries by a process-level worker, and the server exposes honest
readiness, loopback-confined dev auth, bounded HTTP lifecycles, and Prometheus
metrics with fixed-vocabulary labels. Dead-lettered deliveries are operable:
`ants outbox dead-letter list/show/requeue/discard` inspects poison messages
and restarts or terminally discards them under a compare-and-swap fencing
credential, with every intervention committed as event + delivery + audit in
one unit of work (ADR-0015). Retention is bounded and explicit: terminal
rows (`delivered`, `discarded`) are collected by configured horizons in
atomic rounds — delivered victims first, oldest-terminal-first within each
class; never pending, leased, or dead rows, and never events or audit
history — through `ants outbox retention preview/sweep` and, optionally, a
scheduled loop (ADR-0016).

Observability is operational rather than aspirational: every served request
logs one structured, redacted line — normalized route pattern, status,
duration, correlation id (honored from `X-Request-ID` when well-formed,
generated otherwise, always echoed), and a bounded remote class; raw paths,
query strings, headers, bodies, identifiers, secrets, and client addresses
have no code path into logs (ADR-0017). Alert-ready PromQL baselines over
the closed metric set live in the runbooks, and at-least-once outbox
delivery across a hard process kill is proven by an automated
restart-convergence test against disposable PostgreSQL — redelivery without
duplicated logical effects.

Read the [master plan](docs/MASTER_PLAN.md) for the complete product and implementation strategy. The condensed [resources index](docs/RESOURCES.md) links directly to the repositories, documentation, papers, and product research that accelerate development. Architecture decisions live in [docs/adr](docs/adr); the current implementation state and its proof matrix live in [docs/TRANCHE_3_4_EVIDENCE.md](docs/TRANCHE_3_4_EVIDENCE.md), with per-tranche records alongside (latest: [Tranche 3.4 request logging + restart hardening](docs/TRANCHE_3_4_EVIDENCE.md)).

## Quick start

```sh
make build
./bin/ants demo run            # full pipeline: real commits + real test execution
./bin/ants demo run --scm memory --sandbox fake   # scripted variant (no subprocesses)
make ci                        # complete quality gate
```

Serve the HTTP API locally:

```sh
./bin/ants serve --config config/ants.example.yaml
curl -s localhost:8080/healthz
```

The process also exposes Prometheus metrics at `/metrics` on the same
listener (aggregate operational series with fixed-vocabulary labels; disable
with `metrics.enabled: false`, see ADR-0014).

Poisoned deliveries are operated through the CLI (see
[docs/runbooks/outbox-operations.md](docs/runbooks/outbox-operations.md)),
which also covers bounded retention sweeps over terminal rows. Alert-ready
PromQL baselines over the exposed metric series — dead-letter growth,
retry pressure, retention stall, 5xx rate, worker signals — are documented
with their caveats in
[docs/runbooks/alerting-baselines.md](docs/runbooks/alerting-baselines.md).

The API is specified by [openapi/v1/openapi.yaml](openapi/v1/openapi.yaml); TypeScript types are generated into `packages/contracts`.

## Principles

- Plan before writing code.
- Isolate every writer in its own worktree and sandbox.
- Use recursive agents only with explicit depth, cost, time, and concurrency budgets.
- Treat evidence—not agent confidence—as the definition of done.
- Keep self-hosted and managed-cloud deployments on the same open-source core.
- Reuse mature open-source infrastructure instead of rebuilding commodity layers.
- Reject hardcoded demos, fake production paths, and AI-generated slop.

## Layout

| Path | Purpose |
| --- | --- |
| `cmd/ants`, `cmd/api` | CLI and API server binaries |
| `internal/domain` | Entities, typed IDs, error taxonomy, state machines |
| `internal/ports` | Persistence/driver seams |
| `internal/orchestration` | Deterministic run pipeline |
| `internal/sandbox`, `internal/scm` | Driver ports + tranche-1 drivers |
| `internal/policy`, `internal/review` | Capability boundary and ready gate |
| `internal/server` | `/v1` HTTP API |
| `db/migrations` | PostgreSQL schema (embedded, forward-only) |
| `packages/contracts` | Generated TypeScript API types |

## Intended stack

- Go for the control plane, scheduler, node daemon, sandbox lifecycle, and CLI.
- TypeScript and Next.js for the responsive web/PWA experience.
- Expo/React Native for future native mobile clients.
- Firecracker/KVM on Linux and Virtualization.framework/vfkit on Apple Silicon.
- PostgreSQL, Temporal, Keycloak, OpenFGA, OpenTelemetry, and S3-compatible storage.

## License

Apache License 2.0. See [LICENSE](LICENSE).

## Safety

Ants is intended to execute untrusted, AI-generated code. The project is not production-ready yet; the current sandbox is not a security boundary (see [SECURITY.md](SECURITY.md) and ADR-0003). Do not expose early builds to untrusted repositories, credentials, or public networks.
