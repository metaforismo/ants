# Security Policy

## Status

Ants executes AI-generated code. **The current code is not production-ready
and provides no VM-grade isolation.** The tranche-1 sandbox driver confines
commands to a workspace directory and an allow-list; it is explicitly NOT a
security boundary against hostile payloads (see docs/adr/0003).

Do not expose early builds to untrusted repositories, credentials, or public
networks.

## Structural guarantees in the current tree

- Task execution cannot push to remotes or write the default branch: the SCM
  port has no remote operations and policy denies push/merge-to-protected
  structurally (no flag can re-enable them).
- Network access from task execution is denied by policy and no driver
  advertises the capability.
- Secrets never enter task contexts; integration connections store secret
  references only.
- Diagnostics redact secrets (`config.Secret` renders `[REDACTED]`
  everywhere).
- Request logs are structurally redacted (ADR-0017): the logging middleware
  has no code path that can emit raw URLs, query strings, headers
  (including `Authorization` and `Cookie`), bodies, tenant/principal/resource
  identifiers, secrets, or client addresses. Inbound correlation ids are
  grammar-validated before echoing; rejected values are replaced, never
  logged.
- Audit events record every policy decision with actor, action, and outcome.

## Known limitations (accepted for the current tranches)

- Process-level sandbox is not isolation from a motivated attacker.
- Dev header authentication is confined to loopback binds at startup
  (ADR-0013) and must never be enabled outside local development; full OIDC
  replaces it in a later horizon.
- The outbox dispatcher is single-process; multi-node delivery scale-out is
  deferred (ADR-0011, ADR-0013).
- Outbox retention/GC (ADR-0016) deletes only terminal `delivered`/`discarded`
  rows beyond explicitly configured horizons; it is structurally inert by
  default and can never touch pending, leased, or dead rows, domain events,
  or audit history. Manual sweeps run through the local CLI with an explicit
  confirmation flag; scheduled sweeps only start when a horizon is set.
- Dead-letter requeue/discard runs through the local CLI over the store seam:
  whoever can run it holds database privileges, the same trust level as
  `migrate up`. Every mutation is fenced by a compare-and-swap generation and
  committed with its event and audit record atomically; remote operator APIs
  wait for real authenticated principals (ADR-0015).
- `/metrics` is unauthenticated by design like the health probes and serves
  aggregate operational series only (fixed-vocabulary labels; no tenant,
  resource, or principal identifiers). Deployments that must not expose it
  can set `metrics.enabled: false`; an ACL'd admin listener is deferred
  (ADR-0014).

## Reporting a vulnerability

Open a private security advisory via GitHub ("Report a vulnerability") or
contact the maintainers directly — do not file public issues for security
reports. Include reproduction steps and affected commit. We aim to
acknowledge within 3 business days once the project has public infrastructure;
until then, reports are handled by the repository owner.
