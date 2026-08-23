# ADR 0019 — OIDC resource-server foundation: bearer JWT verification, dev-header removal

Status: accepted
Date: 2026-08-23

## Context

ADR-0004 promised since tranche 1 that production authentication replaces the
development posture at the existing `server.Authenticator` seam ("full
removal still lands with OIDC"), and ADR-0013 enforced the loopback bind gate
as a stopgap. The master plan (sections 2, 9.1) names Keycloak as the
reference IdP and OIDC as the authentication mechanism; Horizon 1 item 2 is
"OIDC locale reale".

The repository has no browser client yet: the only consumers of `/v1` are
tests, the CLI, and operators. An Authorization Code + PKCE *browser session*
flow needs endpoints that establish cookie sessions, CSRF protection, and —
above all — a real frontend to drive them. Building that machinery now would
create dead surface with no honest caller and tests that fake a browser by
scraping IdP login HTML. The Tranche 3.5 handoff explicitly authorizes the
alternative: land the largest complete secure vertical slice instead.

This tranche therefore implements Ants as an **OAuth 2.0 / OIDC resource
server**: clients present bearer access tokens issued by a configured
identity provider; the API verifies them strictly and derives tenant and
subject exclusively from verified claims.

## Threat model

Actors and what this seam defends against:

1. **Network attacker without credentials** — must never reach tenant data:
   every authenticated route refuses without a verifiable token (uniform 401,
   RFC 9457 problems, no existence oracles).
2. **Holder of a token minted for another system** (wrong audience), another
   deployment (wrong issuer), or another tenant (claims name a foreign
   tenant): rejected at verification/claim-mapping before any store access;
   cross-tenant reads stay uniform 404s (ADR-0004).
3. **Token forger** — signature, issuer, audience, expiry, not-before, and
   algorithm are all verified server-side against keys fetched from the IdP's
   JWKS endpoint over discovery; `none` and all symmetric algorithms are
   structurally excluded (RS256 allowlist checked before JWS processing, so
   algorithm-confusion attacks have no path into key selection).
4. **Claim smuggler** — tenant and subject come only from claims of a token
   whose signature verified under trusted keys; claim values are
   grammar-checked and re-resolved against the tenant store on every request.
   Nothing is trusted from unverified payloads, headers, or query strings.
5. **Replay after compromise** — accepted tokens are short-lived by IdP
   policy (`exp` enforced with bounded skew); there is no server-side session
   to steal and no refresh material held by Ants.
6. **Denial of service** — token size is bounded, JWKS fetches are
   timeout-bounded and rate-limited (forced rotation refreshes are floored),
   and verification failures are constant-cost after key fetch.
7. **Secret/diagnostic leakage** — the verifier holds no secrets (pure public
   key consumption); tokens never enter logs or error strings (asserted by
   tests); configuration diagnostics keep rendering through
   `config.Secret`/redaction rules.

Trust boundaries touched:

```text
client ──(HTTPS edge)── API [verifies JWT] ──(loopback trust)── store
                          ▲                    ▲
                          │ JWKS + discovery   │ tenant existence check
                     Identity provider         └── tenant scoping unchanged (ADR-0004)
                     (Keycloak, local)
```

The IdP is outside the trust boundary: its metadata and keys are consumed
over HTTP with strict validation. The transport rule — `https` everywhere,
plaintext allowed only for literal loopback hosts so local Keycloak stays
testable without weakening production posture — applies to the issuer, to
the discovery document's `jwks_uri`, and to every redirect target followed
while fetching either. The discovery document's `issuer` must equal the
configured value exactly, with one tolerated difference: a single trailing
slash may appear on either side, mirroring how the `.well-known` URL is
formed from an issuer with a path component (RFC 8414 §4.3); any other
mismatch refuses the provider.

## Decisions

**Bearer access tokens at the existing seam.** The `server.Authenticator`
interface and `Principal` type are unchanged (handlers untouched, ADR-0004
guarantee). The composition root injects either the new OIDC authenticator or
the refusing `UnconfiguredAuthenticator`; nil wiring is a startup error.
Dev-header authentication is **deleted outright** — code, config field, env
variable, OpenAPI scheme — because ADR-0004 made removal part of this
milestone and the injected-authenticator pattern keeps every test honest
without it.

**Verification pipeline** (in order, each failure a typed problem):

1. `Authorization: Bearer <token>` must be present and well-formed; token ≤
   8 KiB. Missing → `missing_bearer_token`; malformed scheme →
   `invalid_authorization_header`.
2. Compact-JWS header `alg` must be exactly `RS256` → else
   `unsupported_token_algorithm`.
3. Signature via JWKS keys (`kid` match). Unknown `kid` triggers one forced
   JWKS refresh (rate-limited to one per second process-wide) then a single
   retry — this is the key-rotation path; persistent failure →
   `invalid_token_signature`. Every IdP exchange, the rotation refresh
   included, runs under `auth.oidc.http_timeout`, so a hung provider can
   stall a request no longer than the configuration allows. JWKS fetch
   failures are transient (`auth_provider_unavailable`, 503).
4. Claims: exact `iss == configured issuer`; `aud` must contain the
   configured audience; `exp` required and unexpired; `nbf` honored;
   `sub` required — all with configurable clock skew (default 30s). Distinct
   codes: `untrusted_issuer`, `audience_mismatch`, `token_expired`,
   `token_not_yet_valid`, `malformed_token`.
5. Mapping to `Principal`: subject = verified `sub`; principal ID =
   `prn_` + SHA-256(issuer ‖ sub) hex truncated to 64 chars — deterministic
   across requests/restarts, collision-resistant, grammar-compliant, and
   free of raw external identifiers. Tenant = configured claim (default
   `ants_tenant`); must be a string matching the tenant-slug grammar → else
   `invalid_tenant_claim`. The store must resolve that slug → else
   `unknown_tenant` (401); store outages surface as transient errors, never
   as authentication refusals.

All rejection codes above render as uniform 401 problems (except explicit
transient kinds) and set `WWW-Authenticate: Bearer` per RFC 6750 when OIDC is
configured.

**Discovery and key lifecycle.** At startup-readiness (and lazily on first
verification) the authenticator fetches `{issuer}/.well-known/openid-
configuration`, requires the document's `issuer` to match the configured
value as specified above, extracts `jwks_uri`, and fetches the key set.
The key set is cached for `auth.oidc.jwks_refresh_interval` (default 15m)
and probed for refresh lazily on subsequent verifications plus forcibly on
unknown `kid`. No background goroutines; every fetch runs under the request or
readiness context with `auth.oidc.http_timeout` bounding it — the rotation
retry included, which is what keeps a hung JWKS endpoint from riding on the
server-level write deadline instead. Restarting the process re-fetches
everything: no auth state survives restart, which is also the restart story.

**Readiness joins the chain.** When OIDC is configured, the composition root
extends `/readyz` with discovery + JWKS warm-up under the existing readiness
timeout, so a misconfigured issuer fails the probe fast instead of surfacing
per-request later; a failing warm-up reports its own typed problem code
(`auth_provider_unavailable`) rather than the generic persistence one, so
the probe names the dependency that actually failed.

**Metrics.** One counter `ants_auth_tokens_total{result}` with a fixed
result vocabulary (accepted, rejected_missing, rejected_header,
rejected_algorithm, rejected_signature, rejected_expired,
rejected_not_yet_valid, rejected_issuer, rejected_audience, rejected_malformed,
rejected_claims, rejected_tenant, provider_unavailable, store_unavailable)
following the ADR-0014 fixed-label rule; identifiers never become labels.

**JOSE dependency: lestrrat-go/jwx v2.** Adopted (master-plan OSS atlas
listed it for evaluation in exactly this role). MIT licensed, actively
maintained, purpose-built for JWS/JWK/JWT parsing with strict algorithm/key
selection; we use only its parsing primitives plus `jwk.Parse` — the cache,
rotation policy, discovery validation, and claim mapping are ours (~150
lines), keeping lifecycle deterministic and fully unit-testable. Recorded in
third_party/manifest.yaml per ADR-0008.

**Testing strategy.** Unit/adversarial suite drives the verifier against
local RSA-signed tokens and httptest IdP fakes: the full rejection matrix
above, skew boundaries, rotation (key swap mid-flight), duplicate-kid and
empty-keyset poisoning, oversized/malformed inputs, cancellation/timeouts,
race stress, and an assertion that no token material reaches any error string
or log output. Integration runs against disposable real Keycloak (Apache-2.0
container, deterministic realm import, no external accounts): client-credentials
tokens (service principals, master plan §9.1) exercise the complete path —
discovery, JWKS, end-to-end tenant-scoped API calls, cross-tenant negatives,
short-lifespan expiry, wrong-audience rejection — through the real composed
server. Direct access grants exist solely inside the fixture realm to mint a
user-shaped token without a browser; they are explicitly not a product
surface.

## Explicit non-goals (deferred, named)

- Authorization Code + PKCE login/callback/session endpoints — lands with the
  web client (Horizon 1 UI wave); the resource-server contract above does not
  change when it arrives.
- Token refresh, revocation lists, introspection (opaque tokens), logout,
  back-channel logout.
- Multi-IdP configuration, per-request IdP selection, SCIM/JIT membership
  provisioning (tenant creation remains today's open bootstrap endpoint —
  documented limitation until memberships ship).
- Device Authorization Grant for the CLI.
- OpenFGA authorization tuples (still ADR-0004 territory).
- Actor-type refinement (service vs human) in event/audit actor fields.

## Consequences

- Production-grade authentication exists and is provable locally for free;
  the development bypass is gone rather than confined.
- Deployments need an OIDC provider to authenticate anything; with none
  configured the API refuses exactly as before (`authentication_not_configured`),
  so nothing silently changes for current users.
- Rotation and restart behavior are lazy-fetch based: first request after an
  IdP key rollover pays one extra JWKS fetch, at most once per second
  process-wide.
- The next tranche inherits a verified identity primitive for sessions
  (web), device flow (CLI), and service-principal management.
