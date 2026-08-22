# AGENTS.md — working rules for agents and contributors

This file orients any agent (human or AI) about to modify the Ants repository.
Read it fully before writing code. `docs/MASTER_PLAN.md` is the product and
architecture source of truth; these are the operating rules that keep the code
base coherent with it.

## Commands

```sh
make ci          # full local gate: fmt, vet, staticcheck, tidy, manifest,
                 # tests, race tests, build, contracts
make test        # unit + contract tests (no external services needed)
make demo        # run the deterministic vertical slice end to end
./scripts/test-postgres.sh   # migration integration tests (needs Docker)
```

`go test ./...` must pass before any commit. `make ci` must pass before a
tranche is declared complete.

## Architecture invariants

- Dependency direction: `cmd → app → services → ports ← adapters`;
  `internal/domain` imports nothing internal. See ADR-0001.
- Tenant scoping: every tenant-owned store call takes an explicit tenant ID.
  Cross-tenant reads return uniform not-found. See ADR-0004.
- State machines live in `internal/domain` as explicit transition tables;
  adding a state without wiring its edges fails `TestTransitionTables*`.
- Policy denials for push/merge-to-default/network/secrets/host-mutation/
  global-install are structural. Do not add flags to re-enable them; extend
  the decision table only with reviewed intent (ADR-0003, ADR-0007).
- The default branch is never written by agent paths. Integration work lands
  on `ants/integration-*` branches.
- Errors carry a taxonomy kind (`domain.ErrorKind`) and stable codes; control
  flow never matches error strings.

## Code standards

- Production-grade Go: no TODOs on runtime paths, no silent fallbacks, no
  unused parameters kept "for later", no wrapper types with one caller.
- Comments explain invariants, safety properties, and non-obvious decisions —
  they do not narrate the code.
- Tests accompany behavior, including negative paths (invalid transitions,
  malformed input, cross-tenant access, cancellation, retry/idempotency).
- Generated files (`packages/contracts/src/schema.d.ts`) are regenerated from
  `openapi/v1/openapi.yaml`, never hand-edited; the OpenAPI spec is the API
  contract and the route table in `internal/server` is pinned to it by test.
- Configuration is typed and validated at startup; no hardcoded hosts, ports,
  paths, tenants, or secrets. Secrets use `config.Secret` so diagnostics stay
  redacted.

## Session protocol for agents

1. Read README.md, docs/MASTER_PLAN.md, docs/RESOURCES.md, this file.
2. Work in small end-to-end tranches; keep the main flow green.
3. Run `make ci` (or at minimum `make test-race`) before finishing.
4. After each tranche: deslop pass limited to the tranche diff, inspect the
   full diff, record evidence (commands + results) in docs, leave a handoff.
5. Record PASS / FAIL / BLOCKED honestly. A check you did not run is BLOCKED,
   not PASS.
6. Never push, deploy, spend money, call paid APIs, install globally, or
   expose network services. Small local commits are fine.
