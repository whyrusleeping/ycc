---
id: "0357"
title: Add replayable coordinator context rollover and safe overflow recovery
status: in_review
priority: 1
created: "2026-09-08"
updated: "2026-09-09"
depends_on:
    - "0356"
spec_refs:
    - §5 Event log
    - §7.1 Provider boundary and history
    - §9.1 Unattended work loop
---

## Description
Coordinator history has no rollover path: context-length failure parks the session and can stop an unattended batch, while retrying the unchanged request cannot succeed. Extend context recovery to the longest-lived agent, complementing subagent rollover in 0321 and output reduction in 0323. Evidence: internal/engine/loop.go:801–810; session/workloop error handling.

## Acceptance criteria
- Support explicit rollover at a safe checkpoint and a bounded safe recovery from actual overflow; never blindly resend the same oversized history.
- Preserve user intent/authorization, decisions, unresolved criteria, verification evidence, artifact references, and active job identities/ownership.
- Record context-view transitions durably so reopen reconstructs exactly the selected model view; original event history remains intact and summaries remain labeled evidence, not new user instructions.
- Handle pending tool results/questions, media/provider continuation state, model switching, and cancelled runs without duplicate mutation or fabricated completion.
- Attended clients expose a meaningful recovery action; unattended execution may continue in a fresh context subject to budgets and genuine blockers rather than treating overflow as an unrecoverable batch failure.
- Regression tests cover long coordinator histories, crash/reopen after rollover, failed summarization, and overflow during unattended work.

## Plan

Implement coordinator rollover as a durable selected-context transition, not transcript deletion or a replay of failed mutations. First trace the existing session/engine safe checkpoints, replay, provider overflow classification, and attended command paths. Add the smallest explicit rollover/recovery surface that can be shared by attended and unattended runs; replace history only after a full tool batch at a durable checkpoint, preserving user authorization and labeled evidence plus job/artifact state. Bound automatic actual-overflow recovery, ensure a genuinely smaller request, and fail safely without replacing context on summary/persistence/cancellation failure. Replay must honor the persisted selected view and continue subsequent events correctly, including pending questions, media/provider state and model switches. Add targeted regression coverage for these persistence and mutation-safety invariants and update the canonical behavior docs. Verify focused packages and an isolated task-only tree; obtain comprehensive review due to replay/ownership risk. Preserve existing unrelated workspace changes via the pre-task snapshot and a selective task commit.

### Starting points
- internal/engine/loop.go: Loop.ContextLengthHandled, steerCheckpoint, Run context-length branch
- internal/engine/replay.go: ReplayHistory tracks pending batches, deferred user messages and lost jobs
- internal/session/session.go: CheckpointMessages and coordinator Run ownership
- Pre-task snapshot at /tmp/ycc-0357-baseline/tree.tar and pre.patch; many unrelated staged edits overlap event/replay/session/spec, do not revert or include them.

## Work log
- 2026-09-09 coordinator handoff: Three implementation/review rounds completed; claude accepts, sol still identifies three concrete blockers. Do NOT mark done or commit yet. (1) Reopen must honor durable session_error action=switch_model before Run: PendingResponse currently reissues the same compact oversized request and resets local contextRecoveryUsed. Add reopen-after-second-overflow regression. (2) After failed automatic summarization or terminal compact overflow, releaseRolloverInputs/generic correction drain must retain accepted input durably without continuing another oversized request; always park with running=false so a later actual model switch can wake it. Test concurrent input during both failure points. (3) selectedViewContainsMedia checks MultiContent and user-input metadata but misses gollama.Message.Images/Documents and durable tool_result images/docs counts; detect structured tool media live and on reopen, not prose substrings. Minor remaining issues: pauseReq during overflow incorrectly forces model-switch recovery; test cancel leak reported by vet in TestFailedOrCancelledIdleRolloverPreservesConcurrentInputForReopen. Existing implementation has durable view events/replay, owner-serialized explicit rollover, authority-preserving fail-closed summaries, client controls and Go+Swift proto regeneration. Explicit rollover intentionally rejects paused/pausing sessions and unreplayable media rather than resuming unsafely. Focused Go/web tests and targeted rollover race tests pass; no Swift toolchain available; unrelated full-package startup-preload race/environment git failures remain out of scope. Preserve unrelated 0344/0345/0355 edits. Pre-task snapshot: /tmp/ycc-0357-baseline/{tree.tar,pre.patch}; task-only clean-HEAD overlay and delta: /tmp/ycc-0357-task-only and /tmp/ycc-0357-task.patch (refresh before committing). Selective task commit required; do not use blanket git add -A.
- 2026-09-09 implementer report: Implemented Task 0357 without committing.  Changes: - Added durable `context_view_changed` events and replay support that replaces only the selected coordinator model view while retaining the complete
…[truncated]
- 2026-09-09 revision: Revised Task 0357 without committing and preserved the unrelated staged/worktree changes.  Implemented: - Routed every explicit context rollover through the session run owner using a request/response
…[truncated]
- 2026-09-09 revision: Completed the final Task 0357 revision without committing and preserved the unrelated staged/worktree changes.  Changed: - Made explicit and automatic rollover input boundaries exact and durable: roll
…[truncated]
