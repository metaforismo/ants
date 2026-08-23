# ADR 0020 — Web console session posture: sealed-cookie BFF over Authorization Code + PKCE

Status: accepted
Date: 2026-08-23

## Context

ADR-0019 landed Ants as an OAuth 2.0 / OIDC **resource server** and deferred
the browser *session* side by name: "Authorization Code + PKCE sessions,
logout, token refresh … deferred until the web/CLI clients that consume them
exist". Tranche 3.7 delivers that first consumer — the `apps/web` Next.js
console operating `/v1` — so the deferred contract is due now, before any of
its shortcuts could calcify.

The console's security problem differs from the API's. The API verifies
bearer tokens statelessly; a browser cannot be handed a bearer token without
creating the exact exposures ADR-0004/0019 closed: any JavaScript that can
read a token can exfiltrate it, and a token in a URL outlives the request.
The console therefore needs a session mechanism where credentials never
reach client-executed code while the API keeps verifying every request
exactly as ADR-0019 specifies — no trust migrates from the API into the web
tier.

## Threat model

Actors this design defends against:

1. **Malicious website driving a logged-in browser (CSRF)** — forged
   cross-site mutations are refused twice: cookies are `SameSite=Lax` (not
   attached to cross-site POSTs) *and* every mutating BFF route requires the
   `Origin` header to equal the validated public origin. The request's own
   `Host` header is deliberately never an acceptance criterion: an attacker
   controlling both headers — the DNS-rebinding shape — can make them agree,
   while the Authorization Code flow already pins every legitimate browser
   to the configured origin byte-exactly through its registered
   `redirect_uri`. Logout — the one route a top-level cross-site form could
   reach — applies the same check.
2. **XSS attempting credential theft** — there is nothing to steal from
   client JavaScript: access/refresh/ID tokens live only inside an
   `HttpOnly` sealed cookie and server memory for the duration of a request.
   No token ever enters `localStorage`, `sessionStorage`, a URL, or a
   client-visible response (the session probe returns identity metadata
   only). Defense-in-depth stays with React auto-escaping and a strict
   CSP-free surface of zero third-party scripts.
3. **Cookie thief** (network eavesdropping or local file read) — the cookie
   value is AES-256-GCM sealed with a deployment key (`ANTS_SESSION_KEY`,
   32 bytes, base64); tampering or truncation yields an unusable session,
   never a partially trusted one. `Secure` is set whenever the public origin
   is https; loopback http fixtures keep cookies deliverable at all.
4. **Open redirector** — post-login redirects accept only relative in-app
   paths (protocol-relative, backslash, control-character, and oversized
   inputs refused), so the login flow cannot be weaponized to bounce users
   off-origin.
5. **Login-flow forger/replayer** — the callback accepts a code only with
   the matching sealed pre-auth transaction: `state` (CSRF), PKCE verifier
   (authorization-code interception), and `nonce` (ID-token replay), each
   single-use; the transaction cookie is cleared before the exchange and
   expires after 10 minutes.
6. **Session fixation** — the session cookie is written fresh at callback
   time only after the token exchange and identity extraction succeed; an
   attacker cannot pre-plant a usable value (it would need to authenticate
   under the deployment key).
7. **Tenant claim smuggler** — whatever the web tier believes, the API
   re-verifies the bearer token and re-resolves the tenant claim against the
   store on every request (ADR-0019 R4); the web tier holds no authority of
   its own to bypass.
8. **Provider outage during logout** — logout clears the local session
   unconditionally first; redirecting to the provider's end-session endpoint
   is best-effort and can fail without leaving the user logged in locally.

Trust boundaries touched:

```text
browser ──(session cookie, no tokens)── Next.js server routes [BFF]
    BFF ──(Bearer access token)── /v1 API [full ADR-0019 verification]
     ▲                                   ▲
     │ Authorization Code + PKCE         │ tenant resolution per request
 Identity provider (Keycloak)            └── store
```

## Decisions

**BFF proxy as the only API path.** The browser talks exclusively to
`/api/v1/*`, which attaches the bearer token server-side and forwards to
configured `ANTS_API_BASE_URL`. Client code imports generated contract types
(`@ants/contracts`) and mints correlation ids itself so `X-Request-ID`
continues browser → BFF → API → events/logs unchanged (ADR-0017/0018).

**One opaque sealed cookie, not a token cache in the browser.** The session
is a JSON document (tokens, expiry, identity, timestamps) sealed with
AES-256-GCM under the deployment key, carried by a single `HttpOnly`,
`SameSite=Lax`, `path=/` cookie whose `Secure` flag follows the public
origin's scheme. Absolute lifetime 8 hours; renewal extends tokens, never
the window. The login transaction uses the same sealing with a 10-minute
life.

**Authorization Code + PKCE (S256) via `openid-client`.** Discovery,
authorization URL, code exchange, ID-token validation (issuer/audience/
nonce), and refresh are delegated to the library; Ants composes them and
never hand-assembles protocol URLs. Identity mapping: subject from the
library-validated ID-token claims; display name from
`preferred_username` (subject fallback); tenant slug from the configured
claim inside the freshly issued access token, grammar-checked before it may
enter a cookie.

**Lazy, serialized silent renewal.** When an API call arrives within the
45-second leeway of access-token expiry and a refresh token exists, the BFF
renews through the provider before forwarding. Renewals are serialized
process-wide (single-flight queue) and re-read the current session after
acquiring the lock, because Keycloak rotates refresh tokens on every use —
two concurrent renewals would invalidate each other. A provider
`invalid_grant` converts to a typed expired-session response so the UI
renders re-authentication instead of retrying forever. Page renders stay
read-only (RSC cannot set cookies): renewal happens only inside route
handlers.

**Uniform failure vocabulary toward the browser.** The BFF maps every
failure onto RFC 9457 problems with stable codes (`session_expired`,
`csrf_rejected`, `api_unreachable`, `web_session_unavailable`); provider and
upstream details stay in server logs. Cross-tenant misses render as uniform
"not available" copy — the web tier preserves the API's no-existence-oracle
posture (ADR-0004).

**RP-initiated logout, local-first.** `POST /api/auth/logout` (origin-
checked) destroys the local cookie unconditionally, then redirects to the
discovery-derived end-session endpoint without `id_token_hint` — the ID
token is deliberately not stored, keeping the sealed session inside the
browsers' ~4 KiB per-cookie budget (writeSession enforces it loudly); the
provider may ask the user to confirm ending its session. Back-channel
logout, front-channel iframe cleanup, and provider-side session sweeping
are not implemented.

**Tenant bootstrap at first login.** Until memberships ship, the first
login under a new tenant claim creates the tenant through the documented
open `POST /v1/tenants` endpoint (probing `unknown_tenant` first;
concurrent-create races treated as success). This is a named limitation of
the current membership model, not a property of the session design.

## Non-goals

- Memberships, roles, invitations, RBAC — the authorization model remains
  "verified claim names your tenant" (ADR-0004) until its own tranche.
- Back-channel/front-channel logout, session management via OIDC
  Session Management spec, token binding/DPoP, sender-constrained tokens.
- Multi-IdP selection UI, SCIM/JIT provisioning, OpenFGA tuples.
- PWA offline mode and service-worker caching of operational data; the
  console is deliberately `no-store` everywhere.
- CLI device flow — separate consumer, later tranche.

## Consequences

- The API contract is untouched: the web tier is one more bearer-token
  client, and non-browser consumers (CLI, tests) work unchanged.
- Operators own one more secret (`ANTS_SESSION_KEY`) and five required
  configuration values, validated fail-closed at first use with every
  problem reported together.
- Refresh tokens rest server-side in sealed cookies; a host compromise
  yields active sessions but no long-term tenant credentials beyond the
  IdP's own refresh-token lifetime policies.
- The renewal queue serializes across the whole process, and renewed states
  are cached process-locally so concurrent requests never replay a rotated
  refresh token; a multi-process or multi-instance console deployment would
  reintroduce cross-instance rotation races and needs a shared renewal
  store before it is safe.
- E2E lifecycle tests use dedicated fixture identities because provider-side
  revocation is user-global: any test revoking sessions must never share a
  user with tests running in parallel.
- E2E proof of the full browser flow (login → operate → revoke → renew)
  requires the disposable Keycloak fixture and is recorded separately in the
  tranche evidence; unit tests pin every decision above that does not need a
  provider.
