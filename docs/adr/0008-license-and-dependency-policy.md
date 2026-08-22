# ADR 0008 — License, reuse, and dependency policy

Status: accepted
Date: 2026-08-22

## Context

Ants is Apache-2.0 with a commercial managed offering (plan section 2). The
plan requires permissive in-process dependencies, verified licenses, pinned
versions, and recorded provenance for every upstream (section 14.8).

## Decision

1. **In-process dependencies** must be Apache-2.0, MIT, BSD, or MPL. Copyleft
   components are acceptable only as separate replaceable services (e.g., a
   self-hosted object store), never linked into Ants binaries.
2. **Provenance**: `third_party/manifest.yaml` records module, pinned version,
   license, adopt/evaluate decision, purpose, and health notes.
   `scripts/manifestcheck` fails CI when a direct go.mod dependency is missing
   from the manifest; the npm side is recorded in the same file.
3. **No copy-paste without provenance.** Code studied upstream is rewritten;
   if code is ever vendored, the commit records upstream revision and license
   header preservation.
4. **Pin everything.** Go via go.mod/toolchain (`go 1.25`, toolchain
   go1.25.5); pnpm exact via `packageManager`; staticcheck pinned by version
   in Makefile and CI.

Current adoptions under this policy: `gopkg.in/yaml.v3` (MIT),
`github.com/jackc/pgx/v5` (MIT), `openapi-typescript` (MIT). Everything else —
Temporal, Keycloak, OpenFGA, Firecracker tooling, Next.js — stays un-adopted
until its tranche, keeping the audit surface minimal.

## Consequences

- Adding a dependency is a reviewed change with a manifest entry, not an
  accident of convenience.
- License re-verification is required at each version bump of any entry.
