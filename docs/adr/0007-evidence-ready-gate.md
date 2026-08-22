# ADR 0007 — Evidence-based ready gate; merge stays human

Status: accepted
Date: 2026-08-22

## Context

The plan's definition of done is evidence, not agent confidence (section 1.3),
with a review loop bounded in rounds and merge never automatic by default
(section 2).

## Decision

The run pipeline ends in an explicit gate:

1. **Spec gate.** A run only leaves planning when the spec declares observable
   success criteria and carries no blockers. Tranche 1 has no human approval
   UI, so the deterministic planner's output auto-passes this gate — recorded
   here as an accepted limitation until the approval surface exists.
2. **Verification evidence.** Success-criteria commands execute against the
   integrated tree (not per-task trees); each produces `Evidence` with command,
   exit code, and a log artifact.
3. **Deterministic reviewer.** Checks: unmet criteria (blocker), missing
   criteria entirely (blocker), forbidden patterns in the diff — TODO/FIXME/
   HACK markers, private-key blocks, AWS key shapes (blockers), oversized diffs
   (warning). Every finding must name a concrete failure scenario.
4. **Ready verdict.** `ready_for_review` requires zero blockers. Blocked runs
   complete their pipeline mechanically but fail the thread with findings in
   the durable report — visible, not hidden.
5. **Merge boundary.** Task work lands on task branches; composition happens
   on a dedicated integration branch (`ants/integration-*`). The default
   branch is never written by any agent path, locally or otherwise. The policy
   engine denies `scm.merge_to_protected` and `scm.push` structurally.

Model-based reviewers replace or augment the deterministic one later through
the same `review.Reviewer` interface.

## Consequences

- The demo's "ready for human review" claim means: criteria executed and
  passed on the integrated tree, no blocking findings.
- Fix/review loops (bounded) are future work; today a blocked review ends the
  thread as failed with full findings rather than looping.
