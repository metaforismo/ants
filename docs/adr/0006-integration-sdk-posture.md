# ADR 0006 — Integration SDK posture: seam now, adapters by wave

Status: accepted
Date: 2026-08-22

## Context

The plan requires full integration coverage per wave (GitHub, webhooks, MCP in
wave A) with real auth, retry, webhook replay, rate limits, and contract tests
— and states plainly that "a nominal adapter does not count" (section 10.3).
Tranche 1 has no network egress at all.

## Decision

No integration adapter code ships in tranche 1 beyond the domain model:
`integration_connection` exists as a versioned entity with its own state
machine (pending → connected → errored/revoked), secret references are stored
as references only, and the SCM port deliberately exposes no remote operations
(push/fetch cannot be expressed, so they cannot be accidentally invoked).

The SCM-host integrations (GitHub PR creation, webhook ingress) arrive in a
later wave as their own packages under `internal/integrations/<provider>` with
capability manifests, fake servers, and failure matrices — per the plan —
behind the policy boundary rather than inside the sandbox drivers.

## Consequences

- The current demo produces local commits on an integration branch; "PR-ready"
  means evidence-complete, not pushed anywhere. Merge stays human (ADR-0007).
- When adapters land, the policy engine's deny table for network access is the
  first thing that must change, with new audit events for every granted scope.
