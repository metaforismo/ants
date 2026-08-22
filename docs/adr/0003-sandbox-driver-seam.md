# ADR 0003 — Sandbox driver seam and the honesty of tranche 1 isolation

Status: accepted
Date: 2026-08-22

## Context

Ants will execute untrusted AI-generated code. Production isolation is
Firecracker/KVM on Linux and Virtualization.framework on macOS (plan section
8), neither of which is available in this environment (`/dev/kvm` absent,
vfkit not installed). The plan's Horizon 1 item 6 calls for a local sandbox
driver with a capability interface and "no fake VM pretenses".

## Decision

`internal/sandbox` defines the driver port with explicit capability
negotiation: drivers declare `Capabilities`, tasks declare requirements, and
admission fails before work starts if they mismatch.

Two tranche-1 drivers:

1. **ProcessDriver** — executes an allow-listed command surface (`sh`, `git`,
   coreutils) inside a per-sandbox directory rooted under a configurable
   `work_root`. It provides workspace confinement, not a security boundary:
   a determined payload can still touch the host. This is stated in the type
   documentation, in config comments, and here.
2. **FakeDriver** — records calls and replays scripted outcomes registered
   from the fixture's declared expectations. It never executes anything;
   unscripted commands fail loudly with `sandbox_unscripted_command`.

Structural rules enforced regardless of driver:

- No driver advertises network capability; there is no configuration flag to
  change this.
- Absolute/relative binary paths are rejected; only allow-listed names resolve
  via PATH.
- Exec output is capped; exec timeouts are classified as retryable-transient,
  cancellation propagates cooperatively.

MicroVM drivers implement the same interface in Horizon 2 and inherit the
same conformance tests.

## Consequences

- The full pipeline is real today: commits are real, verification commands
  really execute (process driver), diffs are real.
- Nobody may claim VM-grade isolation for the current code. Threat-model
  statements about untrusted-code execution wait for Horizon 2 evidence.
