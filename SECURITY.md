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
- Audit events record every policy decision with actor, action, and outcome.

## Known limitations (accepted for the current tranches)

- Process-level sandbox is not isolation from a motivated attacker.
- Dev header authentication is confined to loopback binds at startup
  (ADR-0013) and must never be enabled outside local development; full OIDC
  replaces it in a later horizon.
- The outbox dispatcher is single-process; multi-node delivery scale-out,
  dead-letter requeue/discard tooling, and outbox retention are deferred
  (ADR-0011, ADR-0013).

## Reporting a vulnerability

Open a private security advisory via GitHub ("Report a vulnerability") or
contact the maintainers directly — do not file public issues for security
reports. Include reproduction steps and affected commit. We aim to
acknowledge within 3 business days once the project has public infrastructure;
until then, reports are handled by the repository owner.
