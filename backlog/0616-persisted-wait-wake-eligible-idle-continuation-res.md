---
id: "0616"
title: Persisted wait and wake-eligible idle continuation
status: done
priority: 2
created: "2026-11-26"
updated: "2026-11-26"
depends_on:
    - "0613"
    - "0615"
    - "0619"
spec_refs:
    - docs/design/daemon-restart.md#4. Persisted wait and wake-eligible idle continuation
---

## Description

Slice 4 of restart-safe daemon sessions (`docs/design/daemon-restart.md` §4), building on 0613 (graceful daemon shutdown and phased startup), 0615 (idempotent reopen reconciliation of supervised jobs) and 0619 (startup passive supervision of closed sessions' jobs). Persists an in-flight `wait` call without adding a new agent tool, so a reopen (after a graceful restart, a crash, or a stop) resumes the wait instead of handing the model a "(no result recorded)" placeholder, and confirms the wake-eligible idle reopen rule.

Split (2026-11-26, the authoring work-loop session's decision, not the user's): the original 0616 also covered passive leases and the restart manifest. Those are now 0621 (passive lease ownership and crash-safe job-bound `leases.json`) and 0622 (restart manifest recovery and supervised bash launches on by default in `Serve()`, depending on 0616 and 0621). The parent's criteria for them moved there unchanged.

Note (2026-11-26): 0615 already reopens a session into wake-eligible idle when its last lifecycle boundary is `session_idle(awaiting_jobs)` (or a reopen that recorded `awaiting_jobs`) and `PendingContinuation("coordinator")` holds: `setIdle(true)` + `awaitNextRun(true)`, recorded as `session_reopened.awaiting_jobs` (`reopenAwaitingJobs` in `internal/session/session.go`). This task checks its human-boundary list rather than re-implementing it.

### Acceptance criteria
- `wait` records a durable `wait_started` event carrying `call_id`, resolved `job_ids` (already expanded when omitted), `for`, effective RFC3339 `deadline` (incorporating the 600s default and `ShellWaitCap`), and `shell_only`.
- `resume_wait.go` restores a trailing unresolved coordinator `tool_call(wait)` + `wait_started`:
  - Recovers all undelivered `user_input(queued: true)` corrections pending for that active run in sequence order (including messages queued between `tool_call(wait)` and `wait_started`), seeds them into the correction queue so `InputWake` fires immediately, and delivers them after `tool_result` in live order.
  - Preserves already-recorded `job_claimed` reports without re-emitting `job_claimed`, collects remaining targets, and handles `now >= deadline` via an explicit non-blocking collection (never passing `0` to `waitTool` or `Registry.Wait`).
  - Generalizes `placeholderCallID` for raw-vs-canonical IDs and batch ordering, pre-arms active-turn input routing (`SendInputMessage` -> correction path -> `InputWake`) before publishing the session to the manager map, emits `SessionReopened(wait_restored: true)`, writes `tool_result(name: "wait", restored: true)`, swaps it into history, and continues the coordinator turn.
- Sessions whose last durable lifecycle boundary is `session_idle(awaiting_jobs: true)` (with no intervening human boundary such as pause, refusal, error/model-switch gate, blocked report, fork-first-input, or pending question) and where `PendingContinuation("coordinator")` is true are prepared with `setIdle(true)` before publication and enter `awaitNextRun(true)`; ordinary manual reopens retain `setIdle(false)` and `awaitNextRun(false)`.
- `spec.md` §5.1 and `docs/design/async-jobs.md` describe the persisted `wait`.

## Outcome

wait records wait_started (call id, resolved job ids, for, RFC3339 deadline incl. default and shell cap, timeout_ms, shell_only). Reopen resumes a trailing coordinator wait via resume_wait.go and tools.ResumeWait: it uses the remaining deadline or a non-blocking CollectSuspendable once the deadline has passed, returns already-claimed reports without a second job_claimed, records session_reopened.wait_restored and tool_result(restored), swaps the result into history, and continues the turn. The reopen disposition is decided before publication: wake-eligible idle is armed there, and a finished turn's idle boundary parks the reopen even after a control tool, with stop, error or pause only disarming the wake. Continuing reopens requeue undelivered queued input (session_reopened.requeued_inputs). placeholderCallID matches by index. wait_started has no row in sessionview, web or iOS. spec §3.1/§5.1, async-jobs.md and daemon-restart.md are updated. Leases split to 0621; restart manifest and supervised launches on in Serve split to 0622; 0617 now depends on 0622. Verified with go test ./..., race tests, web vitest/typecheck/build, and YccKit swift test plus xcodebuild on the Mac. sol accepted in round 3.

Commit: session: persist wait (wait_started) and resume it on reopen; arm wake-eligible idle before publication; requeue undelivered queued input (0616; splits leases to 0621 and restart manifest to 0622)
