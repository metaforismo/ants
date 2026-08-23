# Tranche 3 / PR 3.7 — Web console first operable surface: OIDC login, thread list, thread workspace — evidence record

Date: 2026-08-23
Branch: `feat/web-first-surface` → PR against `main`
Base: `main` @ b58572f ("feat: OIDC resource-server foundation …
(ADR-0019) (#20)"), verified equal to `origin/main` before work began.
Environment: macOS arm64 (sandboxed session), Go toolchain 1.25.x with
repo-local `GOPATH`/`GOCACHE`/`STATICCHECK_CACHE` under gitignored
`.local/tranche-3_7/` (HOME never reassigned), pnpm 10.28.0,
Node 22/25, Playwright 1.62.1.

Scope: exactly one bounded outcome from MASTER_PLAN Horizon 1 item 9 per the
Tranche 3.6 handoff — the first operable web surface: identity-provider
login (Authorization Code + PKCE), thread list, thread workspace against the
existing `/v1` API, consuming generated contracts from `packages/contracts`,
with the session contract recorded **before** implementation as ADR-0020.
Deferred by name (handoff non-goals): Expo mobile, automations builder,
billing surfaces, OpenFGA tuples, multi-IdP selection.

**Session honesty note for this record:** this tranche was resumed after an
interrupted session (a Docker daemon probe had hung). On resume, one single
bounded read-only Docker probe was permitted by the operator; it failed
inside its bound, so all Docker-backed suites below are recorded BLOCKED
with the exact cause. Nothing in this record claims a Docker-backed suite
ran. Two compensating proofs exist: an HTTP-level surface inspection
against the real production build (clearly labeled MOCK-BASED, not product
evidence), and the committed browser E2E suite executed remotely by the new
CI job on healthy runners (see "Remote checks").

## Requirement → code → test matrix

| # | Requirement | Code | Tests |
|---|---|---|---|
| R1 | Session contract recorded before implementation | ADR-0020: threat model (8 actor classes), BFF trust-boundary diagram, sealed-cookie decision, CSRF/renewal/logout posture, explicit non-goals | review vs MASTER_PLAN §9.1/§13, ADR-0004/0017/0018/0019 |
| R2 | Authorization Code + PKCE via mature OSS library | `openid-client` 6.8.7 (MIT, adopt recorded in `third_party/manifest.yaml`); discovery, auth-URL construction, code exchange, ID-token validation delegated to the library; Ants never hand-assembles protocol URLs | `tests/config.test.ts`, `tests/session.test.ts`, live flow pinned by committed `e2e/session.spec.ts` + CI `web-e2e` job |
| R3 | No token ever reaches client JavaScript/storage/URLs | BFF-only `/api/v1/*` path attaches bearer server-side (`app/api/v1/[...path]/route.ts`); session = AES-256-GCM sealed JSON inside one `HttpOnly` cookie (`lib/seal.ts`, `lib/session.ts`); `/api/auth/session` returns identity metadata only | `tests/seal.test.ts` (tamper/truncation/fmt), SSR-leak check in HTTP inspection; e2e asserts rail identity without tokens |
| R4 | CSRF posture without a token round-trip | `SameSite=Lax` cookies + same-origin enforcement (`lib/origin.ts`) on every mutating BFF route incl. logout; foreign `Origin` → typed `csrf_rejected` 403 | `tests/origin.test.ts`; HTTP inspection rows 11–12 (cross-origin logout + forged mutation refused) |
| R5 | Login-flow forgery/replay defense | sealed single-use pre-auth transaction (state+nonce+PKCE verifier+redirect, 10 min) cleared before exchange; relative-only redirect policy refusing protocol-relative/backslash/control chars | `tests/origin.test.ts`; e2e anonymous-gating spec |
| R6 | Silent renewal safe under refresh-token rotation | lazy renewal with 45 s leeway, process-wide single-flight queue re-reading current session under the lock, provider `invalid_grant` → typed expired-session state | `tests/session.test.ts` (renewal math, invalid_grant conversion); live rotation + revocation proven by `e2e/session.spec.ts` (CI) |
| R7 | Thread list + workspace operate real `/v1` | generated contract types only (`@ants/contracts`); list/create projects+threads, messages, start-run with forwarded `Idempotency-Key` held stable across retries of one intent, cancel, events resumed from `seq` cursor, terminal report | `tests/components.test.tsx`, `tests/onboarding.test.ts`; operate journey in `e2e/operate.spec.ts` (CI) |
| R8 | Correlation continues browser → BFF → API → events (ADR-0017/0018) | browser mints grammar-valid ids; BFF accepts valid inbound verbatim or replaces, forwards effective value upstream | `tests/requestid.test.ts`; HTTP inspection rows 14–15 (echo + malformed replaced) |
| R9 | First-login tenant bootstrap uses only documented surface | probes `unknown_tenant`, creates tenant once via open `POST /v1/tenants`, treats concurrent create as success | `tests/onboarding.test.ts` (incl. failure paths) |
| R10 | Typed config, fail-closed, nothing hardcoded | `parseWebConfig` validates all five required values + key length + issuer transport rule (https unless literal loopback, mirroring ADR-0019), collects all problems before failing | `tests/config.test.ts` (every failure mode) |
| R11 | Designed states on every surface; status never color alone | loading skeletons, empty states naming next action, typed error+retry, uniform not-available copy for 403/404 (no existence oracle), expired-session re-auth card preserving context, rate-limit handling, shape-coded status glyphs | `tests/components.test.tsx`; DESIGN.md states checklist verified in HTTP inspection |

## Gate results (exact exit codes, final code state)

Exit codes captured into repo-local ignored logs under
`.local/tranche-3_7/gates/`; never inferred from piped output.

| Gate | Command | Exit |
|---|---|---|
| pnpm install (lockfile honest) | `pnpm install --frozen-lockfile --offline` | 0 ("Already up to date") |
| Web typecheck | `pnpm --filter @ants/web typecheck` | 0 |
| Web lint | `pnpm --filter @ants/web lint` | 0 |
| Web unit/component tests | `pnpm --filter @ants/web test` | 0 (42 tests / 7 files) |
| Web production build | `pnpm --filter @ants/web build` | 0 |
| Full hermetic CI (pre-deslop) | `make ci` (fmt, vet, staticcheck, tidy-check, manifest-check, unit, race, build, contracts-test, contracts-drift, web-typecheck/lint/test/build) | 0 |
| Manifest check (after npm entries) | `go run ./scripts/manifestcheck` | 0 |
| Full hermetic CI (final tree, post deslop+polish) | `make ci` | 0 |
| HTTP surface inspection | `.local/tranche-3_7/ui-inspect/http-inspect.mjs` vs production build + loopback mock | 0 (15/15 PASS) — **mock-based, labeled below** |
| Go unit/race/build/contracts | included in `make ci` above | 0 |

## BLOCKED — Docker-backed suites (exact cause)

The operator reported Docker Desktop degraded after a prior disk-full event:
backend processes present, daemon calls hanging. Constraints: no restart,
kill, reset, prune, or disruption of Docker or unrelated user containers.

- Single bounded read-only probe executed exactly once:
  `docker version --format {{.Server.Version}}` under an 8-second
  `execFileSync` timeout, mutating nothing. Result: **failed inside the
  bound** (no daemon answer). No further polling was performed.
- Consequently **BLOCKED locally**: `make web-e2e` /
  `scripts/test-web-e2e.sh` (browser E2E vs disposable Keycloak + real API),
  `scripts/test-keycloak.sh`, `scripts/test-postgres.sh`. These were NOT RUN
  locally in this session; no claim of execution is made. All three scripts
  are committed unchanged in their runnable form so any auditor with a
  healthy daemon can execute them as-is.
- Compensating proof: the new GitHub Actions `web-e2e` job runs
  `scripts/test-web-e2e.sh` on a healthy hosted runner (Keycloak service
  lifecycle via the script itself). Its result is part of the remote checks
  on the PR; see below.

## Surface inspection — clearly labeled MOCK-BASED, not product evidence

Two harnesses live under gitignored `.local/tranche-3_7/ui-inspect/`.

1. **HTTP-level inspection (15/15 PASS)** against the real production build
   (`next start`) with the app's own sealing format minting a fixture
   session cookie and a loopback-only mock implementing the handful of `/v1`
   responses. Proves composed server behavior: anonymous gating
   (`/threads`→`/login?next=…`), login render, authenticated SSR shells, BFF
   proxying, tampered-cookie refusal (`session_expired`), CSRF refusals,
   idempotency-key forwarding, correlation echo/replacement, absence of
   token material in HTML. **Every data value shown by the mock is fixture
   data; none of this is evidence about the product pipeline.**
2. **Browser-dimension inspection: BLOCKED.** Both system Chrome and the
   cached Playwright headless shell abort at launch under this machine's
   security sandbox (`bootstrap_check_in org.chromium.Chromium.MachPortRendezvousServer…:
   Permission denied`). No browser binary can execute here; screenshots and
   interactive checks were therefore not taken, and no download was
   attempted to work around it. The committed Playwright suite covers this
   dimension where browsers run (CI).

## Deslop findings this session (tranche diff only)

- **Fixed a real defect found during inspection:** `/login` and `/` treated
  an *absent* session as authenticated (`.then(() => true)` over a resolver
  that returns `undefined`), producing a redirect loop for anonymous users
  (`/login`↔`/threads`). Fixed to test the resolved value; pinned by HTTP
  inspection rows 1–4.
- Removed dead code with zero production callers: `hooks/use-session.ts`
  (whole module), `problem.ts` runtime parser trio (`ApiError`,
  `isApiError`, `parseProblem`) whose only caller was its own test while
  `client-api.ts` owns the real parsing, and unused export
  `newIdempotencyKey`.
- Un-exported `IDEMPOTENCY_KEY_HEADER` (internal constant).
- One bounded polish: thread-list rows now navigate with Next `<Link>`
  (client-side navigation consistent with the shell) instead of full-page
  anchors. Auth links intentionally remain anchors.
- Gates rerun after every change (`make ci` final exit 0).

## OSS provenance, licenses, links

Adopted (direct, apps/web):

- [openid-client 6.8.7](https://github.com/panva/openid-client) — MIT —
  OIDC relying-party protocol engine (discovery, Authorization Code + PKCE
  S256, ID-token validation, refresh, end-session URL). Session lifecycle,
  cookie sealing, CSRF, identity mapping stay in-repo (ADR-0020).
- [next 16.3.2](https://github.com/vercel/next.js) — MIT — App Router
  server rendering + route handlers hosting the BFF.
- [react / react-dom 19.2.8](https://github.com/facebook/react) — MIT.
- [@tanstack/react-query 5.102.0](https://github.com/TanStack/query) — MIT
  — server-state management (master plan atlas §14.7 role).
- [@playwright/test 1.62.1](https://github.com/microsoft/playwright) —
  Apache-2.0 — committed E2E suite (atlas §14.7).

All recorded with decisions in `third_party/manifest.yaml` (manifest gate
green). No copyleft enters the tree; system font stack only (the earlier
session's external font download was correctly reverted — see audit log).

## Operator-visible changes

- New `apps/web` console: sign-in through your IdP, thread list, thread
  workspace with runs, live event trail, cancellation, terminal report.
- Configuration surface: `ANTS_WEB_URL`, `ANTS_API_BASE_URL`,
  `ANTS_OIDC_ISSUER_URL`, `ANTS_OIDC_CLIENT_ID`, optional
  `ANTS_OIDC_SCOPES`/`ANTS_OIDC_TENANT_CLAIM`, and the required 32-byte
  `ANTS_SESSION_KEY` (seals sessions; treat like DB credentials — rotating
  it invalidates all sessions).
- Fixture realm gains the `ants-web` public client (PKCE S256,
  `ants_tenant` + audience mappers).
- Make targets: `web-deps/-typecheck/-lint/-test/-build/-e2e`,
  `keycloak-integration`, `ci-all`; `ci` stays fully hermetic.
- CI: new `web` job (hermetic gates) and `web-e2e` job (real browser proof
  on hosted runners).

## Known limitations

- Runs anchor per browser tab (`sessionStorage`) because `/v1` exposes runs
  by id only; reopening a thread elsewhere shows truthful status without
  the live panel. Closing the gap needs list-runs-by-thread (named for a
  later tranche).
- Tenant creation remains the open bootstrap endpoint until memberships
  ship (ADR-0020 consequence, SECURITY.md limitation).
- Logout is local-first RP-initiated; back-channel logout, DPoP/token
  binding, device flow: deferred (ADR-0020 non-goals).
- No PWA/offline mode yet; console is deliberately `no-store` everywhere.

## Residual risks (explicit)

- Browser E2E did not run on a developer machine this session (BLOCKED
  above). If the CI `web-e2e` job fails, the PR must not merge until green;
  the failure artifacts upload makes diagnosis direct.
- The mock-based inspection cannot speak to Keycloak form behavior, real
  token lifetimes, or API pipeline semantics — only the committed E2E suite
  (CI) covers those.
- `ANTS_SESSION_KEY` mishandling (reuse across environments, weak value)
  is the main operational risk of the sealed-cookie design; validation
  enforces length only, uniqueness is procedural.
- Renewal serialization is process-wide; a pathological mass-expiry storm
  adds bounded latency (correctness preserved by lock re-read), but very
  long renew queues would benefit from metrics in a later tranche.

## Prompt for PR 3.8 (next tranche)

"You are the sole coding agent for Ants Tranche 3 / PR 3.8. Work in
/Users/francescogiannicola/Documents/Codex/2026-08-22/vo/outputs/ants. Main
must be clean at the squash merge of the web-console PR; verify
status/fetch/exact HEAD and read AGENTS.md, docs/MASTER_PLAN.md,
docs/RESOURCES.md, docs/TRANCHE_3_7_EVIDENCE.md, ADR-0004, ADR-0019, and
ADR-0020 first. Create a small branch; English throughout.

Deliver exactly one bounded outcome chosen from MASTER_PLAN Horizon 1 items
3–4: the durable execution seam exposed end to end in the web console —
list-runs-by-thread on `/v1` (GET /v1/threads/{id}/runs) consumed by the
console so a reopened thread reattaches to its live run without
per-tab sessionStorage anchoring, plus run detail parity (tasks/events/
report) behind the same BFF. Keep the OpenAPI spec authoritative, regenerate
contracts, extend memory+Postgres store parity tests, add cross-tenant
negative tests, and update ADR-0012/ADR-0020 cross-links. Requirements: no
hardcoded tenants/URLs; cursor pagination consistent with existing lists;
loading/error/expired states designed per DESIGN.md; Playwright coverage
extended (reopen-thread-reattaches case) in scripts/test-web-e2e.sh;
correlation and idempotency rules unchanged (ADR-0017/0018).

Use skills as in prior tranches: architect before boundary decisions,
blast-radius before the schema addition, deslop limited to the diff,
show-me-your-work before milestones, handoff at the end. Do NOT implement
memberships/RBAC, automations builder, billing surfaces, or Expo mobile —
name them deferred. Same gates and honesty rules as PR 3.7: hermetic `make
ci` plus Docker-backed suites if the daemon is healthy; record PASS/FAIL/
BLOCKED precisely in docs/TRANCHE_3_8_EVIDENCE.md with commands and exit
codes, update README/SECURITY/runbooks, commit coherently, push, open one
small PR linking evidence, stop before merge, and report URL, head SHA,
gates, limitations, residual risks, and a concrete PR 3.9 prompt."
