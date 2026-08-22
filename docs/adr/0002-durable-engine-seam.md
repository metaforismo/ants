# ADR 0002 — Durable engine seam now, Temporal adoption later

Status: accepted
Date: 2026-08-22

## Context

The master plan names Temporal the single durable workflow engine (section 4.1,
backlog ADR 0002). But Horizon 1 also demands a runnable vertical slice that
works with zero paid services and zero extra infrastructure. Running Temporal
in every developer loop and in CI before the domain logic is stable would add
operational weight without protecting anything.

## Decision

The orchestration pipeline (request → plan/spec → parallel tasks → integration
→ verification → report) is implemented as plain Go functions over the
persistence ports, driven by `internal/orchestration`:

- Every state transition is persisted with optimistic concurrency.
- Every transition emits a versioned event.
- Retries are bounded, classified (`domain.IsRetryable`), and backoff is
  injected via `ports.Sleeper`; cancellation flows through `context`.
- Idempotency keys live on runs; replays return the original run.

When Temporal arrives it wraps these same functions as activities/workflows;
the domain code does not change. The engine refuses double execution of one
run (`registerCancel`, status guard) which matches activity idempotency
semantics.

## Consequences

- Today's engine is single-process: a crash mid-run leaves the run in its last
  persisted state and does not auto-resume. This limitation is explicit and
  accepted for tranche 1; recovery-on-restart lands with the durable engine.
- Tests verify retry/backoff/cancel deterministically via injected sleepers
  instead of real waiting.
