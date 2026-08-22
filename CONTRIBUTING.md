# Contributing to Ants

Thanks for helping build a durable, evidence-driven agent platform.

## Getting started

Prerequisites: Go 1.25+, Node 22+ with pnpm 10 (enabled via `corepack`),
optionally Docker for Postgres integration tests.

```sh
git clone <your fork>
cd ants
go test ./...          # should pass immediately
make demo              # watch the full pipeline run locally
make ci                # the complete quality gate
```

## How we work

- **Tranches, not vibes.** Work lands as small end-to-end slices that keep
  `make ci` green. A tranche ends with a deslop pass over its own diff,
  recorded evidence, and an honest PASS/FAIL/BLOCKED list.
- **The master plan is law.** Product and architecture decisions trace to
  `docs/MASTER_PLAN.md`. Changes to boundaries or contracts need an ADR in
  `docs/adr/` before the code.
- **Evidence over claims.** If you did not run it, it is BLOCKED, not done.

## Code expectations

- Go code follows `gofmt`, `go vet`, and `staticcheck` cleanly; see the
  standards section in [AGENTS.md](AGENTS.md).
- Every behavior change ships with tests, including negative paths:
  invalid state transitions, malformed input, cross-tenant access attempts,
  cancellation, and idempotency replays.
- State machines change only in `internal/domain` transition tables; their
  consistency tests will fail if edges are missing.
- Public API changes require updating `openapi/v1/openapi.yaml` first —
  `TestOpenAPIMatchesRoutes` fails when routes and spec drift apart.
- New dependencies need a license check and an entry in
  `third_party/manifest.yaml` (see ADR-0008). `make manifest-check` enforces
  this for Go modules.

## Commit discipline

- Small, focused commits; imperative subject lines (`orchestration: bound
  retry backoff by task attempts`).
- Never commit secrets, tenant data, or machine-specific paths.
- The default branch is protected by convention: agent-produced integration
  work lands on `ants/integration-*` branches and is merged by humans after
  review.

## Reporting issues

Include: what you ran, what you expected, what happened, and the output of
the failing command. Security-sensitive findings go to SECURITY.md contacts,
never public issues.
