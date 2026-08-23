# Tranche 3.9 evidence — inherited P0 repair and truth synchronization

Date: 2026-08-24

This record covers the completion of the inherited
`feat/run-history-navigator` working tree. It is current execution evidence,
not proof that the broader Ants roadmap, model-driven agents, VM isolation, or
hosted integrations are complete.

## Scope and starting state

- Repository: `github.com/metaforismo/ants`, branch
  `feat/run-history-navigator`.
- Starting `HEAD`: `518a8d68fe77cf02b46db478d87b0ee52cbd5de7`;
  local `main` and `origin/main` resolved to the same commit.
- Nine inherited modified/untracked files were preserved. No reset, clean,
  stash, branch switch, or destructive checkout was used.
- The inherited focused Go packages passed. The inherited web focus had one
  failure (`23 passed / 1 failed`): a stale assertion expected 250 DOM rows
  although the navigator intended a 25-row page. Source inspection also found
  that Older/Newer requests were discarded when the selected run was elsewhere.
- The inherited lint gate reported two `react-hooks/set-state-in-effect`
  errors in the navigator.
- Docker was probed once with a bounded attempt. The client was present, but
  the daemon did not answer and the probe was interrupted; no daemon restart,
  prune, or unrelated container action was attempted.

Public GitHub state was checked read-only: the repository was public and
Apache-2.0, PR #22 and its four checks were green at the starting commit, and
no open PR, issue, or release was found. Hosted evidence applies to the
starting commit only, not to this local tranche.

## Delivered behavior

### B1 — bounded run-history navigation

The navigator now has two independent concepts:

- workspace selection mode: follow the authoritative latest run or pin one
  exact run;
- an explicit page request keyed to the selection id and mode that created it.

The page request is invalidated by derivation when selection/mode changes; no
effect mutates state to reset it. Follow-latest returns to page one when polling
discovers a new latest id. A pin with no explicit browse request follows its
recomputed page as append-only history grows. A pinned operator who browses
away stays on the requested page, clamped only if the history shrinks.

Regression coverage proves empty, 25, 26, and 250-run histories; both pager
directions and all ten pages; a 25-row DOM maximum; exact row pinning;
follow-latest polling; pinned polling and page shifts; browsing away from a
selection; singular/plural newer-run notices; release-to-latest; native button
focus; `aria-current`; and the polite page live region. The existing
reduced-motion browser test remains in the suite, and the real operate journey
now checks the run-history component at 390×844 with no horizontal overflow
and the compact id treatment.

### B2 — typed malformed identifiers without an existence oracle

`validatePrefixed` now returns the stable domain taxonomy
`invalid / invalid_id`. The boundary is pinned across:

- all 16 typed ID parsers;
- all 11 authenticated operations containing an `{id}` path parameter;
- the create-thread `project_id` body reference;
- authentication-before-path-parse precedence;
- well-formed missing resources retaining uniform 404;
- foreign-tenant reads and real mutations retaining uniform 404.

The cross-tenant project assertion now exercises the real
`POST /v1/threads` body boundary instead of a nonexistent project-detail
route. A new OpenAPI test requires every path-ID operation to declare response
400. It failed first on exactly nine operations; those nine responses were
added to the OpenAPI source and regenerated TypeScript contract.

The blast radius is domain-wide: internal ID-generator implementations must
still uphold the prefixed grammar. The production `RandomIDs` adapter does;
documenting and enforcing that port contract is tracked separately rather than
expanding this HTTP-boundary repair.

### B3 — documentation truth boundary

- README no longer says only two tranches exist, calls the current Planner and
  Reviewer deterministic, and separates implemented stack from candidates.
- MASTER_PLAN section 2 is now a four-part ledger: user-confirmed intent,
  implemented facts, recommendations, and open decisions. Temporal, OpenFGA,
  VM drivers, Kubernetes, AI provider, Stripe, Expo, storage, telemetry, and
  integration order are not presented as settled.
- The PostgreSQL composition comment now matches the wired adapter and
  fail-closed behavior. The server package comment no longer mentions a
  deleted development auth mode.
- ADR-0004 retains its historical decision and adds the current OIDC plus
  malformed/well-formed/cross-tenant distinction.
- `IMPLEMENTATION_BACKLOG.md` is the living code-backed P0–P3 matrix. Older
  tranche evidence files were not rewritten.

## Executed verification

| Command | Status | Exit | What it proved |
| --- | --- | ---: | --- |
| `. .local/gate-env.sh; pnpm --filter @ants/web exec vitest run tests/run-history.test.tsx` after adding regressions | FAIL (expected red) | 1 | Five product-behavior failures remained after test-harness corrections: inert paging and its polling/pin consequences. |
| `. .local/gate-env.sh; pnpm --filter @ants/web exec vitest run tests/run-history.test.tsx tests/runs.test.ts` | PASS | 0 | 31/31 focused selection, polling, paging, accessibility, and history-helper tests. |
| `. .local/gate-env.sh; pnpm --filter @ants/web test` | PASS | 0 | 83/83 tests in 10 files; no wider web regression. |
| `. .local/gate-env.sh; pnpm --filter @ants/web lint` | PASS | 0 | Navigator effect lint failures removed; source, tests, and E2E lint clean. |
| `. .local/gate-env.sh; pnpm --filter @ants/web typecheck` | PASS | 0 | TypeScript and DOM test types clean. |
| `. .local/gate-env.sh; GOTOOLCHAIN=local go test -count=1 ./internal/domain ./internal/server` before OpenAPI repair | FAIL (expected red) | 1 | Runtime/domain tests passed; contract test named exactly nine missing 400 responses. |
| Same focused Go command after contract repair | PASS | 0 | Domain parser, HTTP semantics, tenancy, and OpenAPI contract tests pass. |
| `. .local/gate-env.sh; make contracts-generate` | PASS | 0 | `schema.d.ts` regenerated from OpenAPI with openapi-typescript 7.13.0. |
| `git diff --check` | PASS | 0 | No whitespace errors after deslop. |
| `. .local/gate-env.sh; GOTOOLCHAIN=local make ci` | FAIL (diagnostic) | 2 | Incorrectly forced the product toolchain onto Staticcheck; pinned Staticcheck requires Go ≥1.26. |
| `. .local/gate-env.sh; env -u GOTOOLCHAIN make ci` before staging generated schema | FAIL (diagnostic) | 2 | Go/race/build and contract tests passed; the drift target correctly compared regenerated output with the Git index and found the intended unstaged schema. |
| `git add packages/contracts/src/schema.d.ts`, then `. .local/gate-env.sh; env -u GOTOOLCHAIN make ci` | PASS | 0 | Complete canonical non-Docker gate: Go fmt, vet, pinned Staticcheck (auto-selected Go 1.26.7), tidy, manifest, all unit tests, race, binaries, 19 contract tests, generated drift, frozen pnpm install, web typecheck/lint, 83 tests, and Next.js production build. |

The macOS linker printed `LC_DYSYMTAB` warnings while linking some race-test
binaries. The affected packages executed and passed; the canonical gate exited
0. This is recorded as an environment warning, not silently relabeled.

## Blocked or not run

| Check | Status | Precise boundary |
| --- | --- | --- |
| `./scripts/test-web-e2e.sh` | BLOCKED | Requires the Docker-backed disposable Keycloak/API/browser stack; daemon did not answer the one bounded probe. The new mobile assertion is committed to this suite but not locally executed. |
| `./scripts/test-postgres.sh` | BLOCKED | Requires disposable Docker PostgreSQL. Non-Docker store/unit contracts passed; that does not substitute for this integration suite. |
| `./scripts/test-keycloak.sh` | BLOCKED | Requires disposable Docker Keycloak. OIDC unit tests passed; no live-container claim is made. |
| `make ci-all` | BLOCKED | Aggregates the three Docker-dependent checks above. |
| Linux/KVM, physical mobile devices, deployment, live SCM/model/billing providers | NOT RUN | No eligible host/device, no need for this block, and no authority for credentials, egress, payment, or external mutation. |

## Deslop and review

- Removed effect-driven state synchronization in favor of one keyed derived
  state model.
- Kept selection resolution in the existing run helper instead of duplicating
  it in the component.
- Removed redundant visual comments; retained comments only for polling,
  selection, remount, auth, and anti-enumeration invariants.
- Replaced a bogus cross-tenant route assertion with real request surfaces.
- Added one route-table-driven OpenAPI test rather than nine brittle named
  assertions.
- Regenerated contracts from source; no generated file was hand-edited.

## External actions

No push, merge, pull request, issue, release, deployment, package publication,
webhook registration, paid model call, purchase, telemetry egress, live
integration mutation, or Docker cleanup/restart was performed. GitHub access
was read-only. The only staged path before the final gate was the generated
schema required by the repository's drift comparison; the coherent local
tranche was committed only after this evidence and backlog were synchronized.

## Result

Block B is complete for the available environment: all canonical non-Docker
gates pass, the inherited P0 behavior is repaired, contracts and current docs
match reality, and Docker-dependent proof is narrowly marked BLOCKED. Ants is
still not production-ready: model-driven Captain/Builder/Reviewer behavior,
RLM, security-grade sandboxing, membership/authorization, hosted SCM, and the
other backlog capabilities remain future vertical work.
