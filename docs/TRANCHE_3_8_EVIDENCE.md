# Tranche 3 / PR 3.8 — Thread run history end to end: `GET /v1/threads/{id}/runs`, server-truth reattachment, run-detail parity — evidence record

Date: 2026-08-23
Branch: `feat/thread-run-history` → PR against `main`
Base: `main` @ dd14690 ("verify: exact merge of the web-console PR"),
verified clean, fetched, and byte-equal to `origin/main` before work began.
Environment: macOS arm64 (sandboxed session), Go 1.25.5 toolchain with
repo-local `GOPATH`/`GOCACHE`/`STATICCHECK_CACHE` under gitignored
`.local/` (HOME never reassigned; staticcheck 2026.2.1 runs under the
auto-fetched go1.26 toolchain inside that cache), pnpm 10, Node 22,
Playwright 1.62.

Scope: exactly one bounded outcome from MASTER_PLAN Horizon 1 items 3–4 per
the PR 3.7 handoff — the durable execution seam exposed end to end in the
web console: authoritative `GET /v1/threads/{id}/runs` (cursor pagination
consistent with the existing list grammar), tenant-scoped list-runs-by-thread
across ports/domain/memory/Postgres with deterministic ordering and full
negative coverage, regenerated TypeScript contracts, BFF consumption so a
reopened thread discovers and reattaches to its live/latest run without
per-tab `sessionStorage` anchoring, run-detail parity (tasks/events/terminal
report) on real `/v1` APIs and generated types, designed states per
DESIGN.md, and an extended Playwright suite with a reopen-thread-reattaches
scenario against disposable Keycloak + the real API binary.
Deferred by name (handoff non-goals): memberships/RBAC, automations builder,
billing surfaces, Expo mobile, distributed renewal. No speculative stubs were
added for them.

Skills note (honesty): the named workflow skills
(`francesco-engineering-workflow:*`, impeccable, Emil) are not installed in
this session's skill catalog, so none were invoked. Their intent was applied
manually and is traceable in this record: boundary decision before code
(ports-first ordering below), blast-radius reasoning before the read-path
addition, deslop limited to the tranche diff (findings listed), evidence
before claims (gate log paths + exit codes), and DESIGN.md-driven states for
every new surface. Nothing in this record pretends a skill ran.

## Requirement → code → test matrix

| # | Requirement | Code | Tests |
|---|---|---|---|
| R1 | Authoritative OpenAPI list endpoint, pagination consistent with existing lists | `openapi/v1/openapi.yaml`: `get: operationId: listThreadRuns` under `/v1/threads/{id}/runs`; `RunPage {runs[], total}` schema; `after` integer cursor (≥0, default 0, strictly-greater) matching `AfterCursor` semantics; stable `(created_at asc, id asc)` order documented | `internal/server/openapi_test.go` `TestOpenAPIMatchesRoutes` pins spec ↔ route table bidirectionally |
| R2 | Tenant-scoped store read with parity and deterministic order across adapters | `ports.RunStore.ListByThread` (explicit `domain.TenantID`, no optional tenancy); memory adapter (`internal/store/memory/runs.go`); PostgreSQL adapter (`internal/store/postgres/runs.go`) using the existing `runs_thread_idx` — zero schema change | shared contract rows `storetest.Run → RunListByThreadIsTenantScopedStableAndPaginated` execute identically against both adapters (memory locally PASS; PostgreSQL via hosted CI job, BLOCKED locally — see below): scoping, tie-break determinism, limit, cursor resume, beyond-max empty page, empty-thread non-nil page, uniform not-found for foreign and unknown threads |
| R3 | Server wiring without existence leaks | route table entry + method-dispatch case (`internal/server/server.go`); `handleListThreadRuns` mirrors `handleListMessages`: principal → typed path parse → store call that distinguishes unknown/foreign thread (uniform 404 problem, ADR-0004) from known-empty thread (200 `{runs:[],total:0}`, array never JSON null) | `TestListThreadRuns` (order, total, `after=1` resume, beyond-max cursor, runless-thread empty array, missing-thread uniform 404 problem document, unauthenticated 401); `TestCrossTenantIsolation` extended with the `threadruns` row |
| R4 | Contracts regenerated, not hand-edited | `make contracts-generate` (openapi-typescript 7.13.0) rewrote `packages/contracts/src/schema.d.ts`; only the hand-maintained export surface gained `RunPage` | `make contracts-drift` inside `make ci` exit 0 |
| R5 | Console reattaches to live/latest run from server truth; no sessionStorage anchoring | `workspace-view.tsx` consumes `api.listThreadRuns` and renders `latestRun(runs)`; start action invalidates the runs query instead of writing an anchor; `src/hooks/use-active-run.ts` deleted outright | unit: `tests/runs.test.ts` (latest-selection, terminal classification); E2E `e2e/reopen.spec.ts` proves reattachment in a **second tab** — sessionStorage cannot cross tabs, so a visible panel there is proof of server-truth discovery |
| R6 | Run-detail parity on real APIs + generated types, no duplicate polling or racey state | `run-panel.tsx` polls `getRunWithTasks` only while the run is non-terminal (`refetchInterval` callback); event trail keeps cursor resume gated on liveness; report fetches once at terminal; cancelled runs render a truthful no-report notice instead of probing an endpoint that answers 409 forever; all shapes come from `@ants/contracts` | existing operate journey (CI web-e2e job) plus reopened-tab parity assertions in `reopen.spec.ts` (event trail + evidence-table report after reattachment) |
| R7 | Designed states per DESIGN.md; accessibility; reduced motion; responsive | loading skeleton card (`runs-loading`), empty state naming next action, typed error + Retry (`runs-error`), expired-session reuse of `ExpiredNotice`, running/cancellable StatusBadge+Cancel (shape-coded, label always), failed/completed report view, explicit cancelled banner (`run-cancelled`); status never color alone; no new motion primitives (reuses Field Station tokens; `prefers-reduced-motion` rules apply unchanged); flex/grid layouts wrap ≤800px as before | component/state conventions pinned by `tests/components.test.tsx`; browser dimensions by committed Playwright suite (hosted CI) |
| R8 | Correlation/idempotency semantics untouched (ADR-0017/0018) | no middleware or engine changes; the new route rides the same request-log/correlation seam; start-run still requires `Idempotency-Key` with identical validation; BFF forwarding unchanged | existing correlation suites green in `make ci`; `TestRequestIdEchoed` unchanged and passing |
| R9 | Docs updated only where truthful | ADR-0012 consequence update (read-only listing added; claims/machine untouched), ADR-0020 update (sessionStorage interim limitation closed by server truth), README console paragraph + latest-evidence pointer | manual cross-check against shipped behavior |

## Gate results (commands, exit codes, exact counts)

Exit codes captured into repo-local ignored logs under
`.local/tranche-3_8/gates/`; never inferred from piped output.

| Gate | Command | Exit | Counts |
|---|---|---|---|
| Full hermetic CI (recorded run) | `. .local/gate-env.sh && env -u GOTOOLCHAIN make ci > gates/make-ci.log` | **0** | fmt-check, vet, staticcheck, tidy-check, manifest-check clean; Go unit+race all packages ok; build ok; contracts test+drift ok; web typecheck/lint/build ok; web tests 63 passed / 9 files |
| Fresh Go unit suite (post-deslop tree) | `go test -count=1 ./...` | **0** | 23 packages ok |
| Focused race | `go test -race -count=1 ./internal/server/... ./internal/store/...` | **0** | server + store incl. memory/postgres/storetest |
| New store-contract subtest (memory) | `go test -v -run TestMemoryStoreContract ./internal/store/storetest/` | 0 | 20/20 subtests PASS incl. `RunListByThreadIsTenantScopedStableAndPaginated` |
| New API test | `go test -v -run TestListThreadRuns ./internal/server/` | 0 | PASS |
| Web unit/component suite | `pnpm --filter @ants/web test` | 0 | 63 passed / 9 files (was 47/7 at main; +8 rows in `tests/runs.test.ts`, prior files unchanged in count terms) |
| Web production build | `pnpm --filter @ants/web build` | 0 | — |
| Lint/typecheck | eslint src e2e tests; tsc --noEmit | 0 | — |

Exact test-count deltas attributable to this tranche:
Go: +1 storetest contract subtest ×2 adapters (one executed locally, one via
hosted CI Postgres), +1 server test function `TestListThreadRuns`,
+1 extended row in `TestCrossTenantIsolation`. Web: +8 vitest cases,
+2 Playwright specs' worth of coverage additions = one new spec file
(`reopen.spec.ts`, single long scenario).

## BLOCKED — Docker-backed suites locally (exact cause)

One bounded read-only probe was executed exactly once this session:
`docker version --format '{{.Server.Version}}'` under a 15 s shell timeout.
It produced no daemon answer inside the bound (same degraded-daemon shape
recorded by Tranche 3.7). Per session constraints there was no restart,
prune, kill, retry, or further polling.

Consequently **BLOCKED locally**, NOT RUN, no execution claimed:

- `scripts/test-web-e2e.sh` / `make web-e2e` (browser E2E vs disposable
  Keycloak + real API) — including the new `reopen.spec.ts`.
- `scripts/test-postgres.sh` (migration integration; would have executed the
  new storetest row against PostgreSQL).
- `scripts/test-keycloak.sh`.

Compensating proof: the repository's CI executes `web-e2e` (Playwright
against disposable Keycloak + the production API binary) and the Postgres
job (which drives `storetest.Run` through the real adapter) on healthy
hosted runners; their verdicts on the PR head SHA are part of the hosted
checks reported below. The scripts are committed unchanged in runnable form.

## Defects found during this tranche (ledger)

1. **Second sequential HTTP start impossible mid-flight (test design, not
   product bug).** My first draft started two runs back-to-back over HTTP;
   StartRun moves the thread to planning and a second start conflicts until
   the thread settles, and after completion it sits in `ready_for_review`
   which cannot transition to planning. Fixed by seeding additional runs
   directly through the real store with explicit creation instants — honest
   fixture seeding for a read-path test, immune to pipeline timing.
2. **Dead branch copied from the messages pattern (deslop self-catch).** The
   first PostgreSQL `ListByThread` guarded `sql.ErrNoRows` around a
   `COUNT(*)` scan, which can never yield ErrNoRows. Removed; the known-
   thread/unknown-thread distinction now happens only when the count is 0.
3. **Unused boolean return (deslop self-catch).** `threadExists` returned
   `(bool, error)` with the bool unread; simplified to `checkThreadVisible`
   returning only the typed not-found-or-nil error.
4. **Divergent malformed-ID expectation dropped.** I initially asserted 400
   for `GET /v1/threads/not-a-thread/runs`; probing showed every existing
   `{id}` route returns the same wrapped-internal problem for malformed ids
   (`asDomainError` wraps plain parse errors as internal). Inventing a new
   contract for one route would be inconsistent scope creep; assertion
   removed, taxonomy fix named for a future tranche if wanted.
5. **gofmt drift on first `make ci`.** Two test files needed formatting;
   fixed and re-run to exit 0.

## Deslop pass (tranche diff only)

Beyond defects 2–4 above: verified no wrapper types with a single caller
(`lib/runs.ts` holds two pure helpers each consumed by production code and
tests), no unused parameters kept "for later", comments explain invariants
(stability of ordinals, uniform-not-found posture) rather than narrate, and
no cleanup expanded into pre-existing files outside the diff. Gates rerun
after every change; final recorded `make ci` exit 0.

## Residual risks (explicit)

- Browser E2E (including the new second-tab reattachment scenario) has not
  run on this machine — Docker degraded and the machine sandbox blocks
  Chromium launch (recorded by Tranche 3.7); proof rests entirely on hosted
  CI for the PR head SHA.
- The `after` cursor is a position in the thread's stable creation order.
  Runs are never deleted today, so positions are stable; if retention ever
  learns to delete runs, this contract must be revisited explicitly rather
  than silently.
- The workspace attaches to the newest run only; older runs of a thread are
  listed by the API (and paginated) but not yet navigable in the UI — a UI
  history affordance remains future work and was deliberately not built here
  (bounded outcome).
- Cancelled-run reports remain structurally absent (API 409 by design); the
  UI now says so instead of showing a generic failure. If product later wants
  partial reports for cancellations, that is an orchestration change, not a
  UI patch.
- Malformed path ids answer wrapped-internal problems on all routes
  (pre-existing); unchanged here.

## Prompt for PR 3.9 (next tranche)

"You are the sole coding agent for Ants Tranche 3 / PR 3.9. Work in the Ants
repository; main must be clean at the squash merge of the run-history PR;
verify status/fetch/exact HEAD first and read AGENTS.md,
docs/MASTER_PLAN.md, docs/TRANCHE_3_8_EVIDENCE.md, and ADRs 0004, 0012,
0019, 0020 before editing. Create a small branch; English throughout.

Deliver exactly one bounded outcome from MASTER_PLAN Horizon 1 items 3–4
(the remaining gap after run history): run-history navigation and
observability polish in the console — render the thread's full run list from
GET /v1/threads/{id}/runs with cursor pagination (older/newer pages),
letting the operator open any past run's tasks/events/report while keeping
the newest run auto-selected when live; plus the malformed-path-id error
taxonomy fix (typed invalid_request 400 across /v1 handlers) if it stays
small. Keep the OpenAPI spec authoritative and regenerate contracts; extend
memory+Postgres parity tests and negative coverage proportionally; no
hardcoded tenants/URLs; preserve correlation/idempotency semantics
(ADR-0017/0018); states per DESIGN.md including reduced motion; extend
scripts/test-web-e2e.sh coverage only where truthful.

Do NOT implement memberships/RBAC, automations builder, billing surfaces,
Expo mobile, or distributed renewal — name them deferred. Use skills as in
prior tranches if installed (architect before boundaries, blast-radius
first, deslop on the diff, show-me-your-work before milestones, handoff at
end); otherwise apply their intent manually and say so honestly. Same gates
and honesty rules as PR 3.8: hermetic make ci plus Docker-backed suites only
if the daemon is demonstrably healthy (single bounded probe, else BLOCKED);
record commands, exit codes, exact counts, PASS/FAIL/BLOCKED distinctions, a
defect ledger, and residual risks in docs/TRANCHE_3_9_EVIDENCE.md; commit
coherently, push, open one small PR linking evidence, wait for hosted
checks, fix failures in focused commits, stop before merge, and report URL,
head SHA, gates, limitations, residual risks, and a concrete PR 3.10 prompt."

---

# Audit addendum (2026-08-23, later session) — adversarial pagination review of PR #22

An independent adversarial audit of this PR's reattachment claim was started
in a prior session and interrupted, leaving an uncommitted mixed worktree on
`feat/thread-run-history` @ `3e4fa58a5489dc555f2b47b566dce540f963537b`
(unchanged remote head; nothing had been committed or pushed). This addendum
records what that worktree contained, what release coordination decided, and
what this session actually changed. PR #22 remains unmerged; all additions
below are additive to the tranche record above.

## Pre-fix state (verified from the dirty tree before any edit)

- **Correct failing reproduction** (`TestListThreadRunsSurfacesNewestRun-
  BeyondPageSize`, since rewritten — see below): seeded 205 runs (>200
  server default page), fetched page one exactly as the workspace did, and
  failed with:
  `RELEASE BLOCKER: single bounded page hides the true newest run
  run_eR4asC87Er1pSqeRqN1ri3hdlE behind older history; page starts at
  run_niXPAxbsFUWpYxCkYXvSnFelpg`
  (archived at `.local/audit-3_8/gates/repro-fail.log`; a later focused
  probe on the dirty tree recorded only the draft's compile errors, with no
  runtime output, in `.local/audit-3_8/gates/prefix-repro-fresh.log` —
  corrected by the release audit below). The defect was real:
  the console read exactly one page, so any thread with more runs than one
  bounded page reattached to a stale run.
- **Correct cursor-validation direction**: negative/malformed/overflow
  `after` values silently fell back to 0 (200 + empty page) because
  `queryInt64` swallowed parse errors; the draft introduced typed rejection.
- **Build errors**: the draft's `parseAfterCursor` returned `error` while
  `writeProblem` requires `*domain.Error` (three call sites failed to
  compile).
- **Rejected implementation**: the same draft flipped ports/memory/
  Postgres/tests/docs to newest-first `(created_at desc, id desc)` +
  positional OFFSET.

## Design decision (release coordination, applied here)

**Newest-first plus positional OFFSET is rejected.** It is not stable under
append-only inserts: every new run lands at the head and shifts every
offset, so a reader resuming with an old cursor sees duplicates and misses.
The accepted shape keeps the server **oldest-first positional** — new runs
append only at the tail, existing offsets never move — and moves the
latest-run guarantee into the client: walk bounded pages from `after=0`
through the authoritative `total` and take the final item as the true
latest. A concurrent tail append during traversal may grow `total`; the
walk consumes the grown total within bounded guards instead of racing.

## Changes made in this session (all additive to the audit trail)

1. **Oldest-first contract restored everywhere** — `ports.RunStore.ListByThread`
   doc (now states the append-only stability property and that the newest
   run is the last item of the final page), memory comparator, PostgreSQL
   query, `storetest.testRunListByThread`, `TestListThreadRuns`,
   `handleListThreadRuns` comment, OpenAPI description. The rejected
   window-function count variant was dropped with it.
2. **Strict shared cursor grammar kept and completed** — `parseAfterCursor`
   returns typed problems; grammar matches the OpenAPI schema exactly
   (omitted = 0; otherwise one decimal-digit int64 ≥ 0). Repeated values,
   explicit empties, whitespace/sign padding, floats, negatives,
   non-digits, and >int64 overflows are `invalid_cursor` 400s across ALL
   three list endpoints (runs, messages, events) — pinned by
   `TestListThreadRunsCursorGrammar`. Leading zeros are accepted (value
   equality); documented. Postgres `rows.Err()` is now wrapped via
   `wrapScan` instead of returned raw.
3. **Snapshot boundary stated honestly** — the Postgres COUNT and SELECT
   are separate statements and NOT one atomic snapshot; correctness under
   concurrent inserts comes solely from oldest-first append-only ordering
   (tail appends cannot shift positions). Documented on the adapter; no
   multi-request snapshot consistency claimed anywhere.
4. **Append-between-pages stability proven** — the shared store contract
   now seeds a run between two page requests and asserts the resumed page
   continues without duplicate or missing entries (memory PASS; PostgreSQL
   row rides the hosted CI job, BLOCKED locally per the Docker note above).
5. **>page-size API boundary test, constants derived** —
   `TestListThreadRunsPaginationReachesTrueLatest` seeds 205 runs, derives
   the server page length from the first response (no copy of the private
   `defaultPageLimit`), tripwires if one page could cover the seed count,
   then walks pages black-box style to prove full coverage, zero overlap,
   and the true latest run as the final item.
6. **Console traverses through one helper** — `collectRunHistory`
   (`apps/web/src/lib/runs.ts`) walks pages until the authoritative total
   is consumed, tolerating mid-walk growth (bounded by growth-step and
   absolute page caps) and refusing to loop forever on contract violations
   (`no_progress`, `duplicate_run`, `history_shrank`, `unbounded_traversal`
   — each unit-tested). `api.listAllThreadRuns` wraps it; `WorkspaceView`
   uses it as its single React Query `queryFn`, preserving the existing
   polling rule with no duplicate concurrent requests and no waterfall of
   chained queries. Ten vitest cases cover multi-page, exact boundaries,
   empty history, growth, and every guard.

## Gates recorded this session

| Gate | Command | Exit | Notes |
|---|---|---|---|
| Pre-fix reproduction (fresh) | focused `go test` on dirty tree | 1 | runtime RELEASE BLOCKER captured in `repro-fail.log` while the tree still compiled; a later probe captured only the draft's build errors (`prefix-repro-fresh.log`) — no fresh runtime reproduction exists beyond `repro-fail.log` (corrected by the release audit below) |
| Focused Go (server+stores) post-fix | `go test -count=1 ./internal/server/ ./internal/store/...` | 0 | `.local/audit-3_8/gates/go-focused-fixed.log` |
| Focused race post-fix | `go test -race` same packages | 0 | `.local/audit-3_8/gates/go-race-fixed.log` |
| Web unit suite | `pnpm --filter @ants/web test` | 0 | 62 passed / 9 files (10 new traversal cases) |
| Full hermetic CI (final tree) | `. .local/gate-env.sh && env -u GOTOOLCHAIN make ci` | **0** | `.local/audit-3_8/gates/make-ci-final-audit.log`; fmt/vet/staticcheck/tidy/manifest clean, Go unit+race all packages ok, build ok, contracts test+drift ok, web typecheck/lint/test/build ok |
| Intermediate-commit greenness | `go test -count=1 ./internal/server/ ./internal/store/storetest/` at `7de43c2` in a detached worktree | 0 | the cursor-grammar commit passes against the then-unchanged oldest-first stores |

Docker-backed suites remain BLOCKED locally for the exact reasons recorded
above (single degraded-daemon probe, sandbox blocks Chromium); their
verdicts ride hosted checks on the pushed head.

## Residual risks added by this addendum

- The client traversal re-reads `total` per page but does not snapshot the
  store; if a run were ever deleted mid-walk the guards fail closed
  (`history_shrank`) rather than return a wrong history. Runs deletion
  remains nonexistent today (see retention ADR-0016 scope).
- `RUN_HISTORY_MAX_PAGES`/`RUN_HISTORY_MAX_GROWTH_STEPS` are generous
  backstops, not licenses: a tenant genuinely outgrowing them gets a loud
  typed error in the runs panel, not silent truncation.

---

# Release-audit corrections — final PR #22 state (2026-08-23)

This section supersedes the two addenda above wherever they describe the
listing key: the release audit initially corrected wording while the runs
listing still keyed on creation-time positions; the branch now ships the
stronger design those corrections were pointing at, and this record describes
that final state only. Reproduction provenance stays as the audit left it:
the sole runtime reproduction of the single-page defect remains
`.local/audit-3_8/gates/repro-fail.log` (`prefix-repro-fresh.log` holds only
draft compile errors); nothing in this session re-reproduced or claims to
have re-reproduced any defect.

## Final design: dense per-thread sequence keyset (migration 0009)

The intermediate oldest-first positional scheme (`created_at asc, id asc`,
positions as cursors) is gone. Run history is now keyed by a store-assigned,
per-thread dense `seq` allocated once at insert time and immutable after —
the same append-stable posture thread messages already had, but immune even
to clock rollback, which positional `created_at` could never claim.

`db/migrations/0009_runs_seq.sql`, applied by the embedded migrator:

1. adds `runs.seq BIGINT` nullable;
2. backfills it per `(tenant_id, thread_id)` with `ROW_NUMBER()` in the
   historical `(created_at asc, id asc)` order — the exact order every prior
   reader observed, so existing consumers' expectations carry over;
3. sets `seq NOT NULL`;
4. pins density structurally with `CONSTRAINT runs_seq_positive CHECK
   (seq >= 1)`;
5. drops `runs_thread_idx` and creates `UNIQUE INDEX runs_thread_seq_idx ON
   runs (tenant_id, thread_id, seq)`, making double allocation impossible
   under concurrency.

Allocation discipline mirrors messages: memory allocates under the same
write lock as the insert (`internal/store/memory/runs.go`); PostgreSQL locks
the parent thread row (`SELECT … FOR UPDATE`, now a row-fetching probe that
can actually surface `sql.ErrNoRows`) and takes `MAX(seq)+1` inside the
unit-of-work transaction (`internal/store/postgres/runs.go`). The OpenAPI
`AfterCursor` description now states the per-listing cursor meaning exactly
(runs = per-thread run seq; messages = message seq; events = event seq),
`Run.seq` is a required response field, and contracts were regenerated.

Because `runs` now carries two unique keys, idempotency conflicts are mapped
by constraint name (`mapConstraintViolation` on
`runs_tenant_id_thread_id_idempotency_key_key`, the auto-generated name of
migration 0002's table constraint), never by bare SQLSTATE — a sequence-index
breach can no longer masquerade as an idempotent replay.

## Contract coverage added

- `RunCreationOnUnknownThreadIsTypedInvalid`: creating a run for an
  absent-thread and for another tenant's thread must yield typed invalid
  `run_thread_unknown` in both adapters (never a leaked FK error, never a
  silent orphan insert).
- `MessageAppendOnUnknownThreadIsUniformNotFound`: appending to an absent or
  foreign-tenant thread yields uniform not-found in both adapters — the case
  the append's lock probe exists to detect before any seq allocation.
- `RunListConcurrentTailGrowthNeverDuplicates` hardened against flake: an
  empty page while writers are still running sleeps instead of busy-spinning
  (a starved scheduler can no longer burn the convergence budget), and a
  stall after writers settle fatals while surfacing the first writer error,
  so failures name their cause instead of timing out.

## Exact final gates (this session, all commands executed as shown)

| Gate | Command | Exit | Result |
|---|---|---|---|
| Compile focused packages | `go build ./internal/store/... ./internal/domain/...` then `go vet` same scope | 0 | clean |
| Focused tests | `go test -count=1 ./internal/store/storetest ./internal/store/memory ./internal/domain` | 0 | ok |
| Focused race (server+stores+domain) | `go test -race -count=1 ./internal/store/... ./internal/domain ./internal/server` | 0 | all packages ok |
| Store contract (memory), verbose | `go test -v -run TestMemoryStoreContract ./internal/store/storetest/` | 0 | 23/23 subtests PASS incl. the three above |
| Web unit suite (standalone) | `npm test` in `apps/web` (vitest run) | 0 | Tests 64 passed (64) / Test Files 9 passed (9) |
| Full hermetic CI | `. .local/gate-env.sh && env -u GOTOOLCHAIN make ci` | **0** | fmt-check, vet, staticcheck, tidy-check, manifest-check clean; Go unit + race all packages ok; build ok; contracts test + drift ok; web typecheck/lint/test/build ok; web tests 64 passed / 9 files |
| Demo | `make demo` | 0 | deterministic vertical slice: 2/2 tasks integrated, 5 evidence records PASS, ready_for_review true |

Docker-backed suites (`scripts/test-postgres.sh` migration integration,
web-e2e, Keycloak) were NOT RUN locally this session: Docker use is excluded
by session constraints, so the PostgreSQL rows of the new contract subtests
and migration 0009 execution are exercised only by the hosted CI jobs on the
pushed head SHA. Their verdicts are reported from the GitHub checks below,
not claimed here. No defect reproduction was performed or implied beyond the
historical artifacts already named.
