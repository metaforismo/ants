# Tranche 1 — Foundation + deterministic vertical slice: evidence record

Date: 2026-08-22
Environment: macOS 26.x, Apple Silicon (arm64), Go 1.25.5, Node 25.8.2,
pnpm 10.28.0, Docker 29.1.3 (daemon started locally for Postgres evidence).

Every check below lists the exact command and the observed result. A check
that was not executed is recorded as BLOCKED/NOT RUN, not as PASS.

## Gate results

| # | Check | Command | Result |
| --- | --- | --- | --- |
| 1 | Format | `make fmt-check` | **PASS** |
| 2 | Vet | `go vet ./...` | **PASS** |
| 3 | Static analysis | `go run honnef.co/go/tools/cmd/staticcheck@2026.2.1 ./...` | **PASS** (after removing 3 dead-code findings it surfaced) |
| 4 | Tidy check | `go mod tidy` + diff guard | **PASS** |
| 5 | Manifest check | `go run ./scripts/manifestcheck` | **PASS** (pgx/v5 MIT, yaml.v3 MIT documented) |
| 6 | Unit + contract tests | `go test ./...` | **PASS** (11 packages) |
| 7 | Race detector | `go test -race ./...` | **PASS** (after fixing two real races in parallel task execution) |
| 8 | Build | `go build ./cmd/ants ./cmd/api` | **PASS** |
| 9 | Demo (scripted drivers) | `./bin/ants demo run --scm memory --sandbox fake` | **PASS** — completed, ready_for_review=true |
| 10 | Demo (real git + real exec) | `./bin/ants demo run` | **PASS** — 2 tasks, real commits (`e9c7683d…`, `8ae019e8…`), integration branch merged, `bash tests/calc_test.sh` executed for real (exit 0), report written |
| 11 | Contracts generation | `pnpm --filter @ants/contracts generate` | **PASS** (openapi-typescript 7.13.0 → schema.d.ts) |
| 12 | Contracts tests | `pnpm --filter @ants/contracts test` | **PASS** (19/19) |
| 13 | Contracts drift guard | `git diff --exit-code -- packages/contracts/src/schema.d.ts` | **PASS** |
| 14 | Migrations vs real Postgres | `./scripts/test-postgres.sh` (postgres:16-alpine container) | **PASS** — all migrations apply, re-run is a no-op, 14 tables verified present |
| 15 | OpenAPI ↔ routes parity | `go test ./internal/server/ -run TestOpenAPIMatchesRoutes` | **PASS** |

## What the demo proves

The fixture repo starts unable to compute anything (`lib_add.sh`,
`lib_mul.sh` missing; its own test suite fails). One run:

1. Plans from `.ants/capabilities.yaml` (data-driven; unknown requests are
   rejected, not guessed).
2. Produces spec v1 with success criteria; gate refuses criteria-free specs.
3. Executes two isolated tasks in parallel on separate branches with real
   commits (local git driver, fixed identity/dates).
4. Runs each task's verification commands inside its sandbox.
5. Composes branches onto `ants/integration-<runid>` via three-way merge;
   conflicts abort loudly instead of being resolved blindly.
6. Executes the integrated suite (`bash tests/calc_test.sh`) against the
   composed tree — exit codes become evidence records with log artifacts.
7. Applies the deterministic reviewer (criteria coverage, forbidden patterns,
   diff budget) and emits the durable report + `ready_for_review` verdict.

Budget enforcement is visible in every report (`tasks 2/8 exec-ops 5/64`);
exceeding caps fails runs explicitly with blocker findings.

## Negative paths covered by tests (selection)

- Invalid state transitions on all six state machines + table consistency
- Retry budget exhaustion; transient-vs-terminal failure classification
- Cancellation propagates to run/task/thread states cooperatively
- Idempotency replay (sequential and concurrent winner)
- Cross-tenant reads return uniform 404 at store and API level
- Policy denials for push/merge/network/secrets/host-mutation/global-install
  regardless of configuration; unknown actions fail closed
- Sandbox rejects absolute paths and non-allow-listed binaries (a test found
  and fixed a real `/bin/sh` bypass during development)
- Merge conflicts leave the target branch untouched
- Malformed config files / unknown env vars / unknown YAML keys fail startup
- API: missing idempotency key, strict body decoding, auth refusals,
  report-not-ready conflict, cancel-after-finish conflict

## BLOCKED / NOT RUN

| Item | Status | Reason |
| --- | --- | --- |
| Firecracker/KVM driver conformance | NOT RUN | Requires Linux KVM host; no such environment here (per plan §16.3) |
| vfkit/macOS driver | NOT RUN | vfkit not installed; Horizon 2 scope |
| Temporal-backed engine | NOT RUN | Deliberate tranche decision (ADR-0002); port-first |
| PostgreSQL store adapters | BLOCKED→NEXT | Schema + runner proven against PG 16; adapters land next tranche behind existing contract tests (ADR-0009). Until then `store.mode: postgres` fails wiring explicitly |
| GitHub/webhook/MCP integrations | NOT RUN | Wave A scope (ADR-0006); no nominal adapters shipped |
| CI workflow execution | AUTHORED ONLY | `.github/workflows/ci.yml` mirrors `make ci`; pushing to trigger GH Actions was out of bounds for this session |
| OIDC/Keycloak login | NOT RUN | Horizon 1 item 2; dev-header auth is explicit and default-off |

## Honest limitations of this tranche

1. The process sandbox confines workspace + allow-list but is **not** a
   security boundary (ADR-0003). Untrusted-code isolation waits for microVMs.
2. The engine is single-process; a crash leaves the run in its last persisted
   state without auto-resume until the durable engine lands.
3. Spec approval is automatic for criteria-complete specs; human approval UX
   arrives with the review surface.
4. Review fix-loops are not implemented; blocked reviews terminate threads
   visibly with findings rather than iterating.
