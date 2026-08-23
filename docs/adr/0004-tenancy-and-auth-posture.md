# ADR 0004 — Tenant scoping, uniform not-found, and dev-only auth

Status: accepted
Date: 2026-08-22

## Current clarification (2026-08-24)

The title and original context record the first tranche. ADR-0019 superseded
the development-auth portion: the header authenticator and its configuration
were deleted, protected routes now verify OIDC bearer tokens or refuse with
`authentication_not_configured`, and authentication runs before resource-ID
parsing.

Uniform not-found applies to **well-formed** IDs whose resource is absent or
belongs to another tenant, across reads and mutations. An authenticated request
with an ID that violates the public prefix/suffix grammar is instead a typed
`400 invalid_id` client error; this exposes syntax only and creates no resource
existence oracle. Project isolation is exercised through the real
`POST /v1/threads` body reference rather than a nonexistent project-detail
route. Domain, API, cross-tenant, and OpenAPI contract tests pin these
distinctions.

## Context

The plan mandates tenant-scoped IDs, ownership, quotas, policy, audit, and
query filters from the first commit (section 2), plus uniform `404` responses
where anti-enumeration matters (section 9.2). Full OIDC via Keycloak is
Horizon 1 scope; this tranche has no identity provider available.

## Decision

1. **Tenant scoping is structural.** Every tenant-owned repository method in
   `internal/ports` takes an explicit `domain.TenantID`; there is no optional
   tenant parameter anywhere. The memory store filters by tenant on every read
   and write; the SQL schema carries `tenant_id` foreign keys on every
   tenant-scoped table.

2. **Uniform not-found.** Cross-tenant reads are indistinguishable from
   missing resources: stores return `not_found`, never `forbidden`, for
   foreign-tenant IDs. Negative API tests pin this for threads, runs, tasks,
   artifacts, events, and reports.

3. **Auth posture.** Production authentication is refused, not faked:
   - `dev_header_auth: false` (the default) makes every authenticated route
     answer problem code `authentication_not_configured`.
   - Enabling dev headers is an explicit config decision documented as
     local-development-only.
   - OIDC replaces the authenticator interface behind the same seam; handlers
     do not change.

## Consequences

- Tenant-boundary regressions are test failures today, not audit findings
  later.
- The dev-header mode must be removed from production profiles; the readiness
  gate before any SaaS beta includes deleting it or binding it to a build tag.
  Update (2026-08-22, ADR-0013): startup validation now refuses
  `dev_header_auth: true` on any non-loopback bind address, so the
  development posture cannot ship to a reachable interface even by mistake;
  full removal still lands with OIDC.
  Update (2026-08-23, ADR-0019): full removal landed — the dev-header
  authenticator, its configuration field, and its OpenAPI scheme are deleted,
  replaced by the OIDC resource-server verifier at this same seam.
