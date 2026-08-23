# Ants implementation backlog

Current audit date: 2026-08-24. This is the living checklist for work after
`main` commit `518a8d68fe77cf02b46db478d87b0ee52cbd5de7`. Historical tranche evidence
remains immutable; this file records current code, current gaps, and the next
verification target.

Statuses are strict: `PASS` means the named completion command ran
successfully; `FAIL` means it ran and failed; `BLOCKED` means a named
environmental prerequisite was unavailable; `NOT RUN` means it was
deliberately not executed; `IN PROGRESS` and `TODO` describe implementation
state, not verification success.

## Active ownership

The coordinator owned all completed P0 edits. Repository-inventory, research,
run-history diagnosis, and typed-ID blast-radius agents were read-only. Block B
is locally committed on `feat/run-history-navigator`; later implementation
blocks must update this ledger before delegation.

## Ground-truth matrix

| Area | Verified current state | Evidence and boundary |
| --- | --- | --- |
| Git and public delivery | The inherited checkout began on `feat/run-history-navigator` at `518a8d6` with nine dirty/untracked files; Block B is now one verified local commit on top and the worktree is clean. `main` and `origin/main` remain at `518a8d6`; nothing was pushed. The public repository is Apache-2.0; PR #22 is merged; its four hosted checks and the corresponding `main` CI run succeeded; no open PRs/issues or releases were found. | Local Git commands plus read-only GitHub CLI queries on 2026-08-23/24. Hosted results prove only `main`, while Tranche 3.9 records the executed local evidence. |
| Languages and boundaries | Go 1.25.5 control plane; TypeScript 5.9, Next.js 16.3, React 19.2, TanStack Query, Vitest, and Playwright web app. Declared dependency direction is `cmd → app → services → ports ← adapters`; `internal/domain` imports no other internal package. | `go.mod`, `apps/web/package.json`, `AGENTS.md`, ADR-0001. Local Node is 25.8.2 while hosted CI pins Node 22; both satisfy `node >=22`. |
| Canonical local gates | `make ci` covers Go format/vet/staticcheck/tidy/manifest/unit/race/build, contracts test/drift, and web install/typecheck/lint/unit/build. `make ci-all` adds web E2E, PostgreSQL, and Keycloak suites. | `Makefile`. Docker-backed suites are separate and must not be inferred from `make ci`. |
| Hosted CI | Four jobs enforce Go plus PostgreSQL service tests, generated contracts, web gates, and Chromium E2E against disposable Keycloak and the real API. | `.github/workflows/ci.yml`. There is no packaging, deployment, SBOM, vulnerability, or secret-scanning job. |
| Configuration and secrets | Go uses defaults < optional YAML < `ANTS_*` overrides, rejects unknown keys/variables, validates at startup, and loads the PostgreSQL DSN only from a redacting secret value. The web BFF validates required environment variables fail-closed. | `internal/config`, `config/ants.example.yaml`, `apps/web/src/lib/config.ts`. Deployment secret-manager integration is not implemented. |
| Persistence and migrations | Memory and pgx/PostgreSQL adapters implement shared contracts. Nine embedded forward-only migrations cover domain state, UoW/outbox/claims, retention, and dense per-thread run sequences. | `internal/store`, `db/migrations`, `scripts/test-postgres.sh`. PostgreSQL execution is currently `BLOCKED` locally because the Docker daemon did not answer the bounded probe. |
| Authentication | The API verifies OIDC bearer tokens with issuer/audience/RS256/time/tenant checks; the web uses Authorization Code + PKCE and an AES-256-GCM sealed HttpOnly cookie. The former development header bypass is removed. | `internal/authn`, ADR-0019, ADR-0020, `apps/web/src/lib/session.ts`. Local Keycloak integration is Docker-dependent and currently `BLOCKED`. |
| Authorization and tenancy | Store calls are explicitly tenant-scoped and foreign resources return uniform not-found. There is no membership, invitation, role, revocation, or resource-authorization model beyond a verified token naming one tenant; tenant creation remains open bootstrap. | `internal/ports/ports.go`, ADR-0004, `SECURITY.md`. OpenFGA is a proposal, not an implemented or user-confirmed decision. |
| Durable execution | PostgreSQL UoW, transactional outbox, claims, fencing, bounded retries, worker heartbeat/recovery, cancellation, dead-letter operations, and retention exist. The production engine is the repository's worker/claim implementation, not Temporal. | `internal/orchestration`, `internal/worker`, `internal/outbox*`, ADRs 0010–0018. Temporal remains an open direction requiring comparison, not a settled dependency. |
| Planner and reviewer | Production wiring uses a deterministic catalog matcher and deterministic diff/evidence checks. These are truthful local fixtures, not model-driven Captain/Reviewer agents. | `internal/planner/planner.go`, `internal/review/review.go`, `internal/app/app.go`. No model-provider port or RLM runtime exists. |
| Sandbox and SCM | The process driver roots and allow-lists commands but is explicitly not a security boundary. A scripted fake supports canonical tests. SCM supports memory and local Git only; its interface deliberately has no fetch/push/hosted lifecycle. | `internal/sandbox`, `internal/scm`, `SECURITY.md`, ADR-0003. vfkit, Firecracker, snapshots, credential brokerage, and hosted SCM adapters are not implemented. |
| API and contracts | OpenAPI is authoritative and TypeScript declarations are generated. Every one of the 11 path-ID operations now returns and declares typed `400 invalid_id`; malformed body project IDs, valid missing IDs, auth precedence, and foreign mutations have regression coverage. | `openapi/v1/openapi.yaml`, `internal/server/openapi_test.go`, `packages/contracts`. Focused domain/server tests and the full non-Docker gate pass. |
| Web operator surface | OIDC login, thread list/workspace, messages, start/cancel, events, run details, terminal report, and a bounded run-history navigator exist. The navigator renders at most 25 rows, reaches every page, separates browse state from follow/pin state, and remains deterministic across polling. | Focused Vitest `31/31 PASS`; full web `83/83 PASS`; lint, typecheck, and production build pass. A real mobile-layout assertion was added to Playwright, but local browser execution is Docker-blocked. Task tree, diff browser, structured findings, recovery controls, automations, members, policies, and usage remain absent. |
| Observability and limits | Structured redacted request logs, correlation propagation, Prometheus metrics, bounded HTTP lifecycles, budgets, worker concurrency, retries, and output caps exist. `/metrics` shares the public listener and is unauthenticated aggregate data. | ADRs 0013, 0014, 0017, 0018; `internal/metrics`; `internal/config`. A separate protected admin listener and OpenTelemetry traces are not implemented. |
| Supply chain and security gates | Direct Go dependencies must appear in `third_party/manifest.yaml`; Go/JS lockfiles are committed. | `scripts/manifestcheck`, `third_party/manifest.yaml`. Direct JavaScript dependency license coverage, SBOM, vulnerability, secret, provenance, and artifact-signing gates are missing. |
| Egress, credentials, cost, and external state | Canonical tests use deterministic fakes or disposable local services. Live model, GitHub App, billing, deploy, webhook registration, telemetry egress, and paid calls are absent. | Code search plus `SECURITY.md`. This task authorizes no push, PR, deployment, purchase, or live integration mutation. |

## Research evidence boundary

- **Verified source fact:** the authors' [RLM paper](https://arxiv.org/abs/2512.24601)
  and [reference implementation](https://github.com/alexzhang13/rlm) externalize
  prompt context into a programmatic environment with recursive model calls;
  the local execution mode is not a production isolation boundary.
- **Upstream claim:** [Capy documentation](https://docs.capy.ai/llms.txt)
  describes Captain/Build roles, durable threads/tasks, machines, reviews, and
  automations. These are vendor descriptions, not independently verified
  implementation facts or Ants requirements.
- **Verified source fact:** Temporal replays deterministic workflow event
  history; OpenFGA evaluates authorization models plus relationship tuples;
  Firecracker requires Linux KVM and production host hardening; GitHub Apps
  start with no permissions and should request the minimum required; RFC 9700
  is the OAuth 2.0 security BCP.
- **Engineering inference:** each may be evaluated behind Ants-owned ports,
  but none should become a production dependency before a conformance spike,
  threat/blast-radius review, license/provenance record, and explicit decision.

## P0 — regressions, contract truth, and broken gates

### P0.1 — Salvage the inherited run-history navigator

- **Evidence:** the inherited `23 passed / 1 failed` state and two lint errors
  were reproduced. Red regressions then proved inert paging. The replacement
  keyed browse request makes selection/mode changes invalidate stale browsing
  by derivation, without effects or synchronous state resets.
- **Files:** `apps/web/src/app/threads/[id]/run-history.tsx`,
  `workspace-view.tsx`, `apps/web/src/lib/runs.ts`,
  `apps/web/tests/run-history.test.tsx`, `apps/web/tests/runs.test.ts`,
  `apps/web/src/app/globals.css`, and proportionate Playwright coverage.
- **Dependencies:** authoritative oldest-first append-stable run history and
  `resolveSelectedRun`; no API/schema change.
- **Risk/blast radius:** a bad fix can silently move a pin during polling,
  strand follow-latest on history, render unbounded DOM, or make older runs
  unreachable. Selection state and visual page state must remain distinct.
- **Completion:** 0/25/26/large histories, both pager directions, exact
  pinning, follow-latest polling, pinned polling/page shifts, browsing away
  from selection, notices, native keyboard/focus/ARIA, reduced motion, and
  mobile layout are covered; at most 25 rows render; lint is clean.
- **Verification:**
  `. .local/gate-env.sh && pnpm --filter @ants/web exec vitest run tests/run-history.test.tsx tests/runs.test.ts`;
  `pnpm --filter @ants/web test`; `pnpm --filter @ants/web lint`;
  `pnpm --filter @ants/web typecheck`; `pnpm --filter @ants/web build`;
  `./scripts/test-web-e2e.sh` when Docker is healthy.
- **Status:** `PASS` for focused and full non-Docker web gates. Playwright is
  `BLOCKED` locally because the Docker daemon did not answer the bounded probe;
  the mobile run-history assertion and existing reduced-motion suite remain in
  the hosted browser gate.

### P0.2 — Complete typed malformed-ID handling and public contract

- **Evidence:** `validatePrefixed` returns
  `*domain.Error{invalid, invalid_id}`. Tests cover all 16 typed parsers, all 11
  path-ID operations (including append-message), body project IDs,
  auth-before-parse, valid missing IDs, and real cross-tenant reads/mutations.
  A contract test first failed on exactly nine undocumented 400 responses; the
  OpenAPI source and generated TypeScript schema now include all nine.
- **Files:** `internal/domain/ids.go`, `internal/domain/domain_test.go`,
  `internal/server/api_test.go`, `internal/server/openapi_test.go`,
  `openapi/v1/openapi.yaml`, generated `packages/contracts/src/schema.d.ts`.
- **Dependencies:** stable error taxonomy and ADR-0004 uniform not-found.
- **Risk/blast radius:** `validatePrefixed` serves every typed ID, including
  non-HTTP callers. Syntax errors may become client-invalid where callers
  previously wrapped an internal error; valid foreign/missing IDs must remain
  indistinguishable 404s.
- **Completion:** every path-ID operation returns and declares `400 invalid_id`;
  malformed project ID in a body does likewise; every parser is taxonomy-
  typed; valid missing and cross-tenant resources retain uniform not-found;
  generated contracts match OpenAPI.
- **Verification:**
  `GOTOOLCHAIN=local go test -count=1 ./internal/domain ./internal/server`;
  `GOTOOLCHAIN=local go test -count=1 ./...`;
  `GOTOOLCHAIN=local go test -race -count=1 ./...`;
  `make contracts-test contracts-drift`; `make ci`.
- **Status:** `PASS` (focused Go, complete Go, race, contracts, and `make ci`).

### P0.3 — Repair current documentation drift without rewriting history

- **Evidence:** the stale README tranche count, PostgreSQL composition comment,
  server auth comment, ADR-0004 current semantics, and MASTER_PLAN's falsely
  closed architecture choices were verified against code and recovered intent.
- **Files:** `README.md`, `internal/app/app.go`, `docs/MASTER_PLAN.md`, this
  backlog, current tranche evidence and index pointers.
- **Dependencies:** P0.1/P0.2 final behavior and exact gate results.
- **Risk/blast radius:** aspirational prose can be mistaken for implemented or
  user-approved architecture; historical evidence must remain intact.
- **Completion:** current facts, user-confirmed product intent,
  recommendations, and open decisions are explicitly separated; latest
  evidence points to the current tranche; no historical record is rewritten.
- **Verification:** `git diff --check`; link/path review; `make ci`.
- **Status:** `PASS`. README points to the current backlog/evidence; the master
  plan now has four explicit categories; historical tranche files are unchanged.

## P1 — essential product behavior

### P1.1 — First real local Captain/Builder/Reviewer vertical slice

- **Evidence:** current planner/reviewer are deterministic fixtures and there
  is no provider port, repair loop, or model-driven structured specification.
- **Files:** likely `internal/domain`, `internal/ports`, `internal/planner`,
  `internal/review`, `internal/orchestration`, `internal/app`, `internal/server`,
  `cmd/ants`, OpenAPI/contracts, and one proportional web surface.
- **Dependencies:** P0 green; architecture sketch with two viable shapes;
  deterministic local provider for canonical tests.
- **Risk/blast radius:** cross-package state, budgets, cancellation,
  persistence, evidence, and tenant scope. A fake must never be labeled AI.
- **Completion:** user goal → grounded structured spec → bounded task graph →
  builder execution → reviewer findings → bounded repair → evidence report,
  durable and cancellable with explicit step/token/time/concurrency budgets.
- **Verification:** focused unit/contract/failure tests; restart/cancellation
  tests; `make test-race`; `make demo`; `make ci`; disposable PostgreSQL path.
- **Status:** `TODO`.

### P1.2 — Define and prototype Ants RLM semantics

- **Evidence:** no `internal/rlm` exists; ordinary task spawning is not RLM.
  Primary sources externalize context into a REPL and expose recursive calls.
- **Files:** ADR plus bounded packages behind provider/sandbox/artifact/evidence
  ports; no production provider commitment.
- **Dependencies:** P1.1 provider and budget seams; primary-source evaluation.
- **Risk/blast radius:** prompt injection, recursive cost explosion, unsafe code
  execution, replay/provenance ambiguity, tenant leakage.
- **Completion:** ADR defines externalized context, typed host functions,
  recursion/subagents, budgets, cancellation, checkpoints, provenance, and an
  evaluation proving when the prototype beats ordinary decomposition.
- **Verification:** deterministic evaluation corpus; depth/fan-out/token/time
  exhaustion; cancellation/restart/tenant tests; `make test-race`; `make ci`.
- **Status:** `TODO`.

### P1.3 — Decide the durable-workflow direction from failure evidence

- **Evidence:** claims/worker/outbox already implement a durable local engine;
  MASTER_PLAN/ADR-0002 still speak as if Temporal adoption were settled.
- **Files:** blast-radius ADR, orchestration/worker/ports and disposable-service
  tests only after the decision.
- **Dependencies:** failure-injection comparison of retain/harden versus a
  Temporal-backed adapter or a narrowly scoped subset.
- **Risk/blast radius:** duplicate engines, replay incompatibility, state
  migration, cancellation semantics, operational burden.
- **Completion:** one evidence-backed direction covers restart,
  replay/idempotency, cancellation, steering, versioning, and local operation;
  no second production engine is added casually.
- **Verification:** failure injection and restart convergence; race suite;
  relevant disposable-service integration; `make ci`.
- **Status:** `TODO`.

### P1.4 — Membership and resource authorization vertical slice

- **Evidence:** verified tenant claim is the only authority; tenant creation is
  open bootstrap; membership/invite/revoke/roles are absent.
- **Files:** domain, ports, migrations/adapters, authn/authz, server/OpenAPI,
  audit, tests, and a bounded admin UI.
- **Dependencies:** threat model and OpenFGA-versus-owned-model decision.
- **Risk/blast radius:** cross-tenant disclosure, revocation lag, existence
  oracle, migration compatibility, privilege escalation.
- **Completion:** bootstrap, owner/admin/member (or justified alternative),
  invitation/revocation, resource checks, uniform denial/not-found, and audit
  pass memory/PostgreSQL and API non-interference tests.
- **Verification:** store parity; migration integration; security negative
  suite; race; OpenAPI drift; browser flow; `make ci-all` where available.
- **Status:** `TODO`.

### P1.5 — Hosted SCM lifecycle against a local GitHub fake

- **Evidence:** current SCM interface has no remote operations and no hosted
  integration lifecycle.
- **Files:** integration domain/ports/adapter, signed webhook ingress, inbox or
  dedupe persistence, fake server/fixtures, OpenAPI, audit, docs.
- **Dependencies:** minimum-permission GitHub App model; explicit no-egress
  default; membership/run-as boundary.
- **Risk/blast radius:** credential leakage, replay, duplicate PR/check effects,
  rate limits, revocation, branch ownership.
- **Completion:** installation identity, minimum permissions, signatures,
  delivery idempotency, replay, rate limit, revoke, draft PR/check/report
  lifecycle all pass without live credentials or egress.
- **Verification:** fake-server contract/failure matrix, secret-leak scan,
  retry/idempotency/revocation tests, `make test-race`, `make ci`.
- **Status:** `TODO`.

### P1.6 — Sandbox security path and driver conformance

- **Evidence:** process sandbox is explicitly not isolation; SandboxDriver
  lacks snapshot/restore/port/network/resource-limit contracts.
- **Files:** sandbox port/driver tests, ADR/threat model, macOS spike only after
  capability proof.
- **Dependencies:** conformance contract before selecting vfkit or Firecracker;
  Firecracker proof requires Linux KVM.
- **Risk/blast radius:** host escape, filesystem/network leakage, orphaned
  processes/VMs, misleading security claims.
- **Completion:** lifecycle, filesystem, limits, network, cancellation,
  snapshot/restore, ports, artifact extraction, and crash cleanup have one
  fail-closed conformance suite; platform gaps are explicit.
- **Verification:** process/fake conformance; macOS driver tests if implemented;
  Linux/KVM rows `BLOCKED` on Mac until executed on a real eligible host.
- **Status:** `TODO`.

## P2 — operability, UX, maintenance, and assurance

### P2.1 — Complete the operator workspace

- **Evidence:** task tree, diff/evidence browser, structured findings, failure
  recovery/steering, and resource/budget views are missing.
- **Files:** web routes/components plus generated API contracts only after each
  backend capability exists.
- **Dependencies:** P1.1 and design/accessibility review.
- **Risk/blast radius:** UI may imply actions or state the backend does not own.
- **Completion:** every surfaced control maps to a real typed operation and all
  loading/empty/error/offline/auth/rate/mobile/keyboard states are tested.
- **Verification:** unit/component, Playwright desktop/mobile/keyboard/reduced
  motion/axe, production build, `make ci`.
- **Status:** `TODO`.

### P2.2 — Automations with identity, budget, and audit

- **Evidence:** no automation domain/API/UI exists.
- **Files:** domain/ports/migrations/worker/triggers/API/web/runbook.
- **Dependencies:** membership/run-as and durable-workflow decision.
- **Risk/blast radius:** replay, duplicate scheduled effects, disabled user
  execution, unbounded spend.
- **Completion:** schedule/webhook, idempotency, run-as revocation, budgets,
  audit, retry, disable, and history work against deterministic local time.
- **Verification:** clock/replay/restart/concurrency tests; migration suite;
  browser management flow; `make ci-all` where available.
- **Status:** `TODO`.

### P2.3 — Usage metering ledger before billing

- **Evidence:** orchestration budgets exist but no append-only usage ledger or
  price snapshot; Stripe is only a master-plan proposal.
- **Files:** domain/ports/migrations/adapters/API/operator UI.
- **Dependencies:** stable provider/compute meter events.
- **Risk/blast radius:** double charging, silent gaps, authorization leakage.
- **Completion:** idempotent append-only local ledger, reconciliation,
  reservations/releases, and budget hierarchy work without a billing provider.
- **Verification:** store parity, duplicate/replay/concurrency/property tests,
  migration integration, `make ci`.
- **Status:** `TODO`.

### P2.4 — Supply-chain and dependency assurance

- **Evidence:** direct Go manifest exists; direct JS license coverage, SBOM,
  vulnerability, secret, provenance, and signing checks do not.
- **Files:** `third_party/manifest.yaml`, reproducible scripts, CI, security docs.
- **Dependencies:** choose bounded OSS tools with permissive licenses and pinned
  versions; define offline/cache failure behavior.
- **Risk/blast radius:** noisy or network-dependent CI, unreviewed licenses,
  leaked credentials, irreproducible artifacts.
- **Completion:** direct Go/JS provenance and license checks, SBOM, vulnerability
  and secret gates run reproducibly with explicit fail/open policy.
- **Verification:** clean and seeded-bad fixtures for every scanner; CI dry run;
  `make ci` remains hermetic or documents its required cache.
- **Status:** `TODO`.

### P2.5 — Observability topology and failure injection

- **Evidence:** metrics share the API listener; traces, protected admin listener,
  migration compatibility, systematic chaos and performance harnesses are absent.
- **Files:** metrics/server config, runbooks, worker/store failure tests.
- **Dependencies:** deployment topology remains open.
- **Risk/blast radius:** public operational exposure, false readiness, retry
  storms, data loss hidden by happy-path metrics.
- **Completion:** exposure decision, resource limits, failure injection,
  restart/concurrency/migration compatibility, and measured budgets have
  reproducible evidence.
- **Verification:** invalid config, listener, chaos/restart/race, migration and
  bounded performance commands; `make ci` plus applicable integration suites.
- **Status:** `TODO`.

### P2.6 — Keep roadmap and current state mechanically distinct

- **Evidence:** MASTER_PLAN mixes target architecture with implemented facts.
- **Files:** master-plan ledger, README/current matrix, evidence index, optional
  generated capability report.
- **Dependencies:** every completed block updates evidence first.
- **Risk/blast radius:** aspirational support claims reach users and operators.
- **Completion:** every high-impact component is visibly one of implemented,
  user-confirmed, recommended, or open; stale assertions have a failing check
  where feasible.
- **Verification:** doc/link checks, source-to-matrix audit, `git diff --check`.
- **Status:** `PASS` for the current truth pass. The ledger, README, backlog,
  ADR clarification, and evidence index now separate target architecture from
  implemented state; future capability changes must keep them synchronized.

### P2.7 — Enforce the internal ID-generator contract

- **Evidence:** every public parser is taxonomy-typed and production
  `RandomIDs` emits valid grammar, but `ports.IDGenerator` does not state or
  enforce that a nil-error result must match the requested prefix. A faulty
  internal adapter could therefore surface an `invalid_id` client
  classification for a server defect.
- **Files:** `internal/ports/ports.go`, ID adapters, orchestration creation
  seams, and adapter contract tests.
- **Dependencies:** retain the public malformed-ID semantics completed in P0.2.
- **Risk/blast radius:** domain-wide creation paths for runs, specs, tasks,
  events, and artifacts; changing classification must not weaken HTTP 400s for
  actual client input.
- **Completion:** the port documents prefix/grammar guarantees, every adapter
  passes conformance, and an injected malformed nil-error generator fails as an
  internal adapter violation rather than a client-invalid request.
- **Verification:** focused ports/orchestration/server failure tests;
  `GOTOOLCHAIN=local go test -count=1 ./internal/ports ./internal/orchestration ./internal/server`;
  `make ci`.
- **Status:** `TODO`.

## P3 — optional breadth and later research

### P3.1 — Production VM fleet, snapshots, object storage, and signed previews

- **Evidence:** none are complete production systems.
- **Files:** future data-plane services, drivers, protocols, deploy and runbooks.
- **Dependencies:** P1.6 conformance, threat model, eligible Linux/KVM proof,
  object-store license/operations decision.
- **Risk/blast radius:** highest: host isolation, credentials, network exposure,
  retention and destructive cleanup.
- **Completion:** cross-platform conformance, hardened hosts, image provenance,
  snapshot compatibility, signed/revocable grants and crash cleanup proven.
- **Verification:** dedicated macOS and Linux/KVM matrices, security/chaos/perf,
  recovery drills; no simulated row reported as platform PASS.
- **Status:** `TODO`.

### P3.2 — Additional integrations and provider routing

- **Evidence:** GitLab, Bitbucket, Slack, Linear, Jira, Vercel, MCP, BYO model
  subscriptions, and model routing are roadmap only.
- **Files:** one adapter vertical at a time behind Ants-owned contracts.
- **Dependencies:** P1.5 integration kit, authz, metering, egress approval.
- **Risk/blast radius:** external side effects, secrets, provider churn, cost.
- **Completion:** each adapter has auth, revoke, rate-limit, webhook/replay,
  contract fake, audit, docs and UI before being listed as supported.
- **Verification:** adapter-specific fake matrix plus opt-in live tests only with
  action-time authorization and credentials.
- **Status:** `TODO`.

### P3.3 — Managed deployment, billing provider, Expo, multi-region

- **Evidence:** Kubernetes, Stripe, deployment topology, definitive AI provider,
  Expo client, multi-region, and cold storage are not decided or implemented.
- **Files:** intentionally unspecified until preceding interfaces stabilize.
- **Dependencies:** explicit product decisions, security/operations maturity,
  metering and capability contracts.
- **Risk/blast radius:** one-way architecture, money, external state, compliance,
  device/platform divergence.
- **Completion:** one bounded decision and vertical slice at a time; no provider
  or topology is adopted from the old plan by default.
- **Verification:** decision-specific; device proof is distinct from simulator
  or export; payment/live-deploy tests require explicit action-time approval.
- **Status:** `TODO`.
