# Ants

Ants is an open-source, durable software-engineering agent platform built around planning, isolated execution, recursive collaboration, evidence, and human control.

Like an ant colony, specialized agents divide work, operate independently, share durable facts, and combine their results into a verified outcome.

## Status

Ants is currently in the architecture and implementation-planning phase. The repository starts from a detailed, evidence-bounded master plan rather than a disposable prototype.

Read the [master plan](docs/MASTER_PLAN.md) for the complete product and implementation strategy. The condensed [resources index](docs/RESOURCES.md) links directly to the repositories, documentation, papers, and product research that can accelerate development.

## Principles

- Plan before writing code.
- Isolate every writer in its own worktree and sandbox.
- Use recursive agents only with explicit depth, cost, time, and concurrency budgets.
- Treat evidence—not agent confidence—as the definition of done.
- Keep self-hosted and managed-cloud deployments on the same open-source core.
- Reuse mature open-source infrastructure instead of rebuilding commodity layers.
- Reject hardcoded demos, fake production paths, and AI-generated slop.

## Intended stack

- Go for the control plane, scheduler, node daemon, sandbox lifecycle, and CLI.
- TypeScript and Next.js for the responsive web/PWA experience.
- Expo/React Native for future native mobile clients.
- Firecracker/KVM on Linux and Virtualization.framework/vfkit on Apple Silicon.
- PostgreSQL, Temporal, Keycloak, OpenFGA, OpenTelemetry, and S3-compatible storage.

## License

Apache License 2.0. See [LICENSE](LICENSE).

## Safety

Ants is intended to execute untrusted, AI-generated code. The project is not production-ready yet. Do not expose early builds to untrusted repositories, credentials, or public networks.
