# Tranche 3 / PR 3.6 — OIDC resource-server foundation: bearer JWT verification, dev-header removal — evidence record

Date: 2026-08-23
Branch: `feat/oidc-resource-server` → PR against `main`
Base: `main` @ 505bc74 ("feat: request correlation propagation into events
and audit records (ADR-0018) (#19)"), verified clean and equal to
`origin/main` before work began (`git status` empty, HEAD match).
Environment: macOS arm64, Go toolchain 1.25.5 (staticcheck 2026.2.1
auto-switches to go1.26.7), Docker 29.1.3 with disposable
`quay.io/keycloak/keycloak:26.4.1` (Apache-2.0) and `postgres:16-alpine`,
pnpm/openapi-typescript 7.13.0.
Sandbox note: this environment forbids writes under `~/go`; every gate ran
with repo-local `GOPATH`/`GOCACHE`/`STATICCHECK_CACHE` under gitignored
`.local/tranche-3_6/` (HOME never reassigned).

Scope: exactly one bounded outcome from MASTER_PLAN Horizon 1 item 2
("OIDC locale reale") per the Tranche 3.5 handoff: production-grade
authentication at the existing `server.Authenticator` seam. The repository
has no browser client, so the full Authorization Code + PKCE *session* flow
has no honest consumer; per the handoff's explicit alternative, this tranche
lands the largest complete secure vertical slice — strict bearer JWT
resource-server verification — and defers login/session machinery by name
(ADR-0019 non-goals). Delivered as ADR-0019
(`docs/adr/0019-oidc-resource-server.md`), written before implementation.

## Requirement → code → test matrix

| # | Requirement | Code | Tests |
|---|---|---|---|
| R1 | Contract grilled and recorded first | ADR-0019: threat model (7 actor classes), trust boundaries diagram, verification pipeline order, key lifecycle, explicit non-goals | human review vs MASTER_PLAN §2/§9.1/§15, ADR-0004/0008/0013/0014/0017/0018 |
| R2 | Handlers unchanged behind the seam (ADR-0004) | `server.Authenticator`/`Principal` untouched; new `server.Deps.Auth` required non-nil (nil = startup error); composition root (`cli.newServer`) picks OIDC verifier or `UnconfiguredAuthenticator{}` | build graph; `TestDisabledMetricsAllowNilCollector`-style construction paths all pass Auth |
| R3 | Standards-compliant verification | Discovery fetch with byte-exact issuer match (OIDC Discovery §4.3); JWKS cache: lazy TTL refresh + unknown-kid forced refresh floored at 1/s; RS256 allowlist checked on the compact header BEFORE key selection (algorithm confusion unreachable); exact iss, aud containment, exp required, exp/nbf with configured skew, sub required — claim classification done in-repo, jwx used for JWS/JWK crypto only (`jwt.WithValidate(false)`) | `TestAuthenticateAdversarialMatrix` (13 rows incl. alg:none, HS256 confusion, oversized, tampered payload, rogue key, missing kid), `TestAuthenticateClaimMatrix` (12 rows incl. skew-boundary accepts), provider-failure suite |
| R4 | Tenant + subject only from verified claims, enforced end to end | subject=`sub`; principal id = `prn_`+SHA-256(issuer‖sub) hex (64 chars, grammar-valid, deterministic); tenant = configurable claim (default `ants_tenant`), must be a string passing `domain.ValidateSlug`, then resolved via `Tenants.GetBySlug` on EVERY request → `unknown_tenant` 401; store outage → transient 503, never an auth refusal | `TestPrincipalIDDeterministicAndNamespaced`; live `TestKeycloakEndToEndRun` (run.principal == thread creator == derived id); live cross-tenant uniform-404 suite; unit unknown-tenant row |
| R5 | Dev-header removal (ADR-0004 condition) | `DevHeaderAuthenticator`, `X-Ants-Dev-*`, `dev_header_auth` config field + env var deleted; OpenAPI `devHeader` scheme replaced by `bearerAuth` (http bearer JWT) on all authenticated routes; example config rewritten | full-suite green without the mode; `TestUnconfiguredAuthRefusesEverything` pins refusal + no challenge advertised when unconfigured; contracts-drift gate 0 |
| R6 | Config validation, typed errors, safe defaults | `auth.oidc.{issuer_url,audience,tenant_claim,jwks_refresh_interval,clock_skew,http_timeout}` + `ANTS_AUTH_OIDC_*` env layering; all-or-nothing partial-config rejection; https-required-unless-literal-loopback issuer rule; duration bounds; distinct problem codes: missing_bearer_token, invalid_authorization_header, unsupported_token_algorithm, malformed_token, invalid_token_signature, untrusted_issuer, audience_mismatch, token_expired, token_not_yet_valid, missing_subject, missing_exp, invalid_tenant_claim, unknown_tenant (+ transient auth_provider_unavailable / tenant_store_unavailable); RFC 6750 `WWW-Authenticate: Bearer` only when OIDC configured | `TestOIDCIssuerConfinedToLoopbackOverHTTP`, `TestOIDCPartialConfigurationRejected`, `TestOIDCEnvLayering`, 7 OIDC rows in `TestValidationFailures`; live rejection matrix incl. wrong-audience client |
| R7 | Rotation/restart behavior | forced refresh on any verification failure (rate-limited), stale cache serves through IdP outages, no background goroutines, no auth state across restart | `TestKeyRotation` (new kid verifies after rotation, revoked kid dies, floor bounds fetches), `TestRestartRefetchesEverything`, stale-cache outage subtest |
| R8 | No secret/token leakage; cancellation; observability | verifier holds no secrets; tokens have no path into error strings/logs (test-asserted); every IdP exchange runs under request ctx bounded by `auth.oidc.http_timeout`; readiness joins discovery+JWKS warm-up; `ants_auth_tokens_total{result}` fixed-vocabulary counter via Observer seam (ADR-0014 pattern) | `TestErrorsNeverCarryTokenMaterial`, `TestReadinessJoinsProviderState`, `TestObserverRecordsOutcomes`, `TestConcurrentVerificationUnderRotation` (-race), live smoke grep of logs for the presented token |
| R9 | Correlation propagation intact under authenticated traffic (3.5 contract) | middleware chain unchanged; carrier produced before auth dispatch | live: sent `X-Request-ID` echoed in response AND persisted verbatim in the planning-transition event's `trace_id`, while worker-phase events keep empty trace ids (ADR-0018 semantics hold under OIDC) |

## Gate results (exact exit codes, final code state)

Exit codes captured directly into repo-local ignored logs under
`.local/tranche-3_6/`; never inferred from piped output.

| Gate | Command | Exit |
|---|---|---|
| Format | `gofmt -l $(git ls-files '*.go')` | clean (no output) |
| Vet | `go vet ./...` | 0 |
| Lint | `go run honnef.co/go/tools/cmd/staticcheck@2026.2.1 ./...` | 0 |
| Tidy idempotence | `go mod tidy` twice vs checksummed copies (`cmp`) | identical (GM_OK/GS_OK); `make ci` tidy-check 0 post-commit |
| Manifest | `go run ./scripts/manifestcheck` | 0 ("all direct dependencies are documented") |
| Unit | `go test ./...` | 0 (23 packages ok) |
| Race | `go test -race ./...` | 0 (23 packages ok) |
| Focused stress | `go test -race -count=60 ./internal/server/ ./internal/authn/ ./internal/config/ ./internal/correlation/` | 0 (server 80.5s, authn 358s at ×60 core re-run) |
| PG16 integration | `./scripts/test-postgres.sh` (disposable postgres:16-alpine; migrations + contract suites + restart-convergence test) | 0 (postgres package 13.09s) |
| Keycloak integration | `./scripts/test-keycloak.sh` (disposable keycloak:26.4.1 --import-realm; guarded suite `-race`) | 0 (authn package 8.52s incl. live proofs) |
| Full CI | `make ci` (fmt-check, vet, lint, tidy-check, manifest-check, test, test-race, build, contracts-test, contracts-drift) | 0 (re-run on the committed tree) |
| Build | `make build` | 0 (bin/ants, bin/ants-api) |
| Demo | `make demo` | 0 (real pipeline, evidence PASS lines) |
| Live-binary smoke | `.local/tranche-3_6/smoke-3_6.sh` (real `ants-api serve` + OIDC env + fixture Keycloak) | 0 — 11 checks PASS |

## Smoke checklist (all PASS)

build · serve with ANTS_AUTH_OIDC_* · healthz · readyz 200 proves the
readiness chain includes IdP warm-up · unauthenticated request → 401 +
`WWW-Authenticate: Bearer` + `missing_bearer_token` problem · garbage
credential → 401 `malformed_token` · real client_credentials token → tenant
bootstrap 201 → authenticated project list 200 · other-service token cannot
see acme's projects (tenant scoping under real signatures) · X-Request-ID
echoed under authenticated traffic · `ants_auth_tokens_total{result=...}`
exposed · presented token absent from server logs · SIGTERM exit 0.

## OSS provenance, licenses, links

Adopted (direct):

- [github.com/lestrrat-go/jwx/v2 v2.1.7](https://github.com/lestrrat-go/jwx/v2) — MIT — JWS/JWK/JWT parsing primitives (signature verification with strict kid+alg key selection, JWKS parsing). Listed in the master-plan OSS atlas §14.4 for evaluation in exactly this role; decision recorded as **adopt** in `third_party/manifest.yaml`. Hand-rolled JOSE rejected (ADR-0008: reuse mature components for security-critical primitives).

Transitive (all resolved by Go modules, licenses verified at adoption):
`github.com/goccy/go-json v0.10.6` (MIT), `github.com/lestrrat-go/blackmagic v1.0.4` (MIT),
`github.com/lestrrat-go/httpcc v1.0.1` (MIT), `github.com/lestrrat-go/httprc v1.0.6` (MIT),
`github.com/lestrrat-go/iter v1.0.2` (MIT), `github.com/lestrrat-go/option v1.0.1` (MIT),
`github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.1` (ISC), `github.com/segmentio/asm v1.2.1` (MIT),
`golang.org/x/crypto v0.53.0` (BSD-3-Clause). All permissive; no copyleft enters the tree.

Services/fixtures:

- [Keycloak 26.4.1](https://www.quay.io/repository/keycloak/keycloak) (`quay.io/keycloak/keycloak:26.4.1`) — Apache-2.0 — disposable local container only; deterministic realm import at `scripts/keycloak/realm-ants.json`; no external accounts. Client secrets inside the fixture realm are throwaway loopback-only values.
- Standards relied on: [RFC 6750](https://datatracker.ietf.org/doc/html/rfc6750) (bearer), [RFC 7515/7517](https://datatracker.ietf.org/doc/html/rfc7517) (JWS/JWKS), [OpenID Discovery](https://openid.net/specs/openid-connect-discovery-1_0.html) §4.3 (issuer validation), [OAuth 2.0 client_credentials](https://datatracker.ietf.org/doc/html/rfc6749#section-4.4) (fixture grants).

## Deslop findings this session

- Removed unused `compactHeader.Typ`, unread `a.doc` field, draft helper
  slop in tests (`observerFunc` superseded by mutex-safe `resultCapture`),
  dead `buildServer` wrapper, misleading `devHeaders` test helper renamed to
  `authedHeaders`, and a stray placeholder var block in `discovery.go`.
- Fixed a genuine data race found by `-race`: the authenticator read the
  lazily-initialized key-store pointer outside its mutex during the rotation
  retry; `keySet` now returns the store handle it fetched with.
- Two clock sources unified: `keyStore.fetch` stamps cache time from the
  injected clock instead of `time.Now()`.
- Staticcheck U1000 findings (unused helpers) resolved rather than silenced;
  no nolint comments added anywhere.

## Operator-visible changes

- Authenticated `/v1` routes now require `Authorization: Bearer <token>` from
  the configured identity provider. With none configured they refuse with
  `authentication_not_configured` exactly as before — but the development
  bypass is gone, not confined.
- New configuration surface: `auth.oidc.*` (YAML or `ANTS_AUTH_OIDC_*`).
- New metric: `ants_auth_tokens_total{result}` (fixed result vocabulary).
- `/readyz` additionally warms discovery/JWKS when OIDC is configured.
- OpenAPI: `bearerAuth` security scheme documented on all authenticated
  routes with the full rejection-code vocabulary.

## Known limitations

- Authorization Code + PKCE sessions, logout, token refresh, revocation,
  introspection, device flow: deferred until the web/CLI clients that consume
  them exist (ADR-0019 non-goals). The resource-server contract does not
  change when they land.
- Tenant creation remains an open bootstrap endpoint until memberships/admin
  ship; tokens can only act inside the tenant their verified claim names.
- Event envelopes carry no actor field today (engine-wide pre-existing
  shape): durable attribution of verified identity rides the run row's
  `principal` and thread `creator_id`, both asserted live. Actor typing
  (human vs service) refinement is deferred with the membership model.
- Live realm-level key rollover was NOT exercised against real Keycloak
  admin APIs; rotation is proven at unit level with swapped JWKS documents
  plus the rate-limit bound. The lazy-refresh design makes the live case a
  subset of the tested one (unknown kid → one forced refetch), but the live
  proof stays explicitly NOT RUN.
- Multi-IdP, SCIM/JIT provisioning, OpenFGA tuples: deferred (ADR-0019).

## NOT RUN / BLOCKED

None BLOCKED. NOT RUN items are named above (live IdP rollover) and are
unit-covered subsets otherwise. Every gate listed executed to completion
against the final committed code state; containers were removed afterwards
(`docker ps` shows no ants-* containers), scratch/config files under
`.local/tranche-3_6/` remain gitignored and are removed after evidence
capture.

## Prompt for PR 3.7 (next tranche)

"You are the sole coding agent for Ants Tranche 3 / PR 3.7. Work in
/Users/francescogiannicola/Documents/Codex/2026-08-22/vo/outputs/ants.
Main must be clean at the squash merge of the OIDC resource-server PR;
verify status/fetch/exact HEAD and read AGENTS.md, docs/MASTER_PLAN.md,
docs/RESOURCES.md, docs/TRANCHE_3_6_EVIDENCE.md, ADR-0004, ADR-0013, and
ADR-0019 first. Create a small branch; English throughout.

Deliver exactly one bounded outcome chosen from MASTER_PLAN Horizon 1 item 9
— the first operable web surface: onboarding dev login, thread list, and
thread workspace against the existing /v1 API, consuming generated
contracts from packages/contracts. The API now authenticates with OIDC
bearer tokens (ADR-0019); the web app obtains tokens through Keycloak
Authorization Code + PKCE using a mature, permissively licensed OIDC client
library (record adopt/decision + license in third_party/manifest.yaml and
an ADR covering session handling, cookie flags, CSRF, logout, and silent
renewal posture). Requirements: Next.js App Router under apps/web; design
system original (no Linear/Capy clone), keyboard-first, WCAG 2.2 AA floor;
loading/error/empty/unauthorized/rate-limited states designed, reduced-motion
respected; every mutation carries Idempotency-Key where the API requires it;
event stream consumption resumes from seq cursors; correlation ids flow from
the browser through X-Request-ID so log/event equality continues end to end
(ADR-0017/0018); no secrets in client bundles; Playwright E2E against the
disposable Keycloak fixture (reuse scripts/test-keycloak.sh patterns)
proving login → create project/thread/message/start run → observe terminal
report, plus unauthorized and expired-session states.

Use these skills at the appropriate moments: [@Emil Design Skills]
(plugin://emil-design-skills@personal) — especially emil-design-eng — for
interaction/motion quality with purpose; $impeccable (init/new-work for
PRODUCT.md/DESIGN.md alignment, then shape/harden/audit/adapt and ONE
bounded polish pass); francesco-engineering-workflow:architect before
boundary decisions; blast-radius before schema/protocol changes; deslop
limited to the tranche diff after implementation, then rerun gates;
show-me-your-work before declaring milestones; handoff at the end with
PASS/BLOCKED boundaries. Do NOT implement Expo mobile, automations builder,
billing surfaces, OpenFGA tuples, or multi-IdP selection — name them
deferred. Constraints: no paid services, no external network beyond the
local stack, no hardcoded tenants/URLs/secrets (typed config only), no
`any`/TODOs on runtime paths. Same gates and honesty rules as PR 3.6
(Go gates unaffected but must stay green; add web typecheck/lint/build/
Playwright gates; make ci extended accordingly); record evidence in
docs/TRANCHE_3_7_EVIDENCE.md with commands and exit codes, update README/
SECURITY/runbooks/ADR cross-links, commit coherently, push, open one small
PR linking evidence, stop before merge, and report URL, head SHA, gates,
limitations, and a concrete PR 3.8 prompt."
