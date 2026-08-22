# ADR 0001 — Module boundaries and the dependency rule

Status: accepted
Date: 2026-08-22

## Context

The master plan (section 5.1) requires that domain logic never imports
providers, HTTP handlers, database drivers, or vendor SDKs, and that adapters
implement ports declared against the domain. Without an enforced structure,
agent platforms rot into a tangle of "utils" packages and implicit coupling.

## Decision

Tranche 1 consolidates the pure model into `internal/domain` and declares all
external seams in `internal/ports`:

```
cmd/*            → internal/app → internal/{server,orchestration,...} → internal/ports ← internal/{store/*,sandbox,scm}
internal/domain  → imported by everything; imports nothing internal except itself
```

Rules enforced by review and by `go list`-based checks as the tree grows:

1. `internal/domain` has zero internal imports and zero infrastructure deps.
2. Adapters (`store/memory`, `store/postgres`, `sandbox`, `scm`) may import
   `domain` and `ports`; nothing imports adapters except `internal/app`.
3. `internal/app` is the only composition root; binaries stay thin.
4. No package named `utils`, `common`, or `helpers`.

Deviation from the plan's sketch (`internal/threads`, `internal/tasks`,
`internal/spec` service packages): those services do not yet exist as distinct
behaviors — CRUD lives behind store ports and orchestration owns lifecycle.
The packages will be extracted when they accumulate real behavior, not before.

## Consequences

- Swapping any adapter is a wiring change in one file.
- Domain tests run without network, disk, or clocks.
- The dependency rule is currently social; add a lint gate when more than a
  handful of contributors touch the tree.
