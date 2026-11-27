---
id: "0632"
title: Backlog task deferral (not_before), work-loop wait guidance, and capped-waiter state fix
status: done
priority: 2
created: "2026-11-27"
updated: "2026-11-27"
depends_on: []
spec_refs:
    - spec.md#6.2 Backlog
    - spec.md#9.1 Unattended work loop
---

## Description

On 2026-11-27, all 9 live sessions in a `wheeler` work loop (`parallel = 4`, `max_waiting = 8`) were parked on background shell jobs, starving 23 ready `todo` tasks. Only 1 session was running active compute (~30–45 min vector pack); the other 8 were sleeping across multi-hour or multi-day observation windows (24 h to 7 d) or tailing `journalctl --follow` indefinitely for an unscheduled event.

Three gaps produced this:
1. **No task deferral primitive.** Backlog tasks have no `not_before` timestamp. If a loop session records a future check-in time and exits without a running job, the task stays actionable and the next loop slot immediately picks it up again. Spawning a long `sleep` job is the only mechanism an agent has to hold a task out of the queue until a future time.
2. **Loop prompt does not distinguish active jobs from multi-hour/multi-day waits.** `workLoopPrompt` (`internal/session/workloop.go`) directs sessions to end their turn on any long-running shell job ("a run or deploy due much later") without noting that waiter slots are scarce (`max_waiting`), or instructing agents to defer future observation windows, use `depends_on` for task dependencies, or mark `blocked` for unscheduled external/human events.
3. **Capped-out idle foreground slot reports as working/running.** When `wl.waiters` is already at `max_waiting`, an idle foreground session awaiting shell jobs cannot pass `reserveWaiting()` (`internal/session/workloop.go:1362`) and remains in its foreground lane (`lane.run != nil`). As a result, its slot reports `state: "working"`, `hasForegroundLocked()` stays `true`, and the loop reports `state: "running"` instead of `waiting` (`sessions_waiting`), even when every live session is idle on shell jobs.

Related: 0597 (work loop moves on while a session waits on its own shell jobs, done in `b4ea60b`). Direction accepted by the user on 2026-11-27 in this chat session.

### Scope

1. **Backlog task `not_before` deferral** (`internal/docs/docs.go`, `internal/orchestrator/orchestrator.go`, `internal/session/workloop.go`, `spec.md` §6.2 and §9.1):
   - Add optional `not_before` (RFC 3339 UTC timestamp, e.g. `2026-11-28T00:05:00Z`) to task YAML frontmatter; omit when empty.
   - Extend `EligibilityFor` (with an explicit clock / `EligibilityAt` helper for deterministic testing) so a `todo` or `in_progress` task with `now < not_before` is not actionable, with a dedicated eligibility gate naming the due timestamp. Once `now >= not_before`, the task becomes actionable automatically without modifying the file.
   - Surface `not_before` and the deferred gate in `list_backlog` and `get_task` output, and reflect the non-actionable state in daemon `BacklogTaskSummary.Ready`.
   - Accept optional `not_before` on `create_task` and `update_task` (RFC 3339 timestamp to set; empty string on `update_task` to clear), rejecting invalid timestamps. Updating `not_before` on a focused task counts as progress (`focusedTasksChanged`).
   - When the work loop has no immediately ready tasks and no working/waiting sessions, and open deferred tasks remain, finish cleanly and name the deferred tasks and earliest `not_before` in the loop outcome/digest rather than hanging or claiming no tasks exist.
2. **Work-loop waiting and deferral guidance** (`internal/session/workloop.go`, `spec.md` §9.1):
   - Update `workLoopPrompt` (and tool descriptions for `create_task`/`update_task` as needed) to distinguish the three wait classes:
     - Active compute or short waits (minutes to ~1–2 hours): background shell job + end turn.
     - Scheduled future check-ins / observation windows (hours to days): set `not_before` on the task (or commit ready code/deploy work and create a deferred check-in task with `not_before`) and exit cleanly rather than sleeping in a shell job.
     - Waiting on another task or an unscheduled external/human event: use `depends_on` or `blocked` with an explicit unblock condition rather than sleeping or holding indefinite `follow` streams.
3. **Truthful state when a foreground session is idle at `max_waiting`** (`internal/session/workloop.go`):
   - When a foreground lane session is idle awaiting only its own shell jobs (past `waitGrace`) and cannot detach solely because `wl.waiters` is at `max_waiting`, report that slot's state as `"waiting"` rather than `"working"`.
   - When no foreground lane is actively working and no waiting session is awake, report the loop state as `"waiting"` (`sessions_waiting`) rather than `"running"`.
   - Preserve existing admission and cap invariants (`len(waiters) >= max_waiting` still blocks new starts, and reclaiming/collecting the foreground session still accounts for its record).

## Acceptance criteria

- `internal/docs`: `not_before` round-trips through YAML frontmatter (omitted when unset), rejects invalid RFC 3339 values on tool writes, and `EligibilityFor`/`EligibilityAt` marks future `not_before` tasks non-actionable and past/equal `not_before` tasks actionable without mutating the file.
- `create_task` and `update_task` set and clear `not_before`; `list_backlog` and `get_task` show deferred status and timestamp; `BacklogTaskSummary.Ready` is false while deferred.
- Work loop skips deferred tasks until `not_before` passes, counts deferring a focused task as progress, and finishes with an outcome naming deferred tasks and earliest due time when only deferred tasks remain.
- `workLoopPrompt` states the distinction between active background jobs, `not_before` deferrals, `depends_on`, and `blocked`.
- A foreground loop session idle on shell jobs at the `max_waiting` cap is reported as `"waiting"` in `WorkLoop.Slots`, and a loop whose foreground and waiting sessions are all idle on shell jobs reports `"waiting"` (`sessions_waiting`) rather than `"running"`, covered by unit tests in `internal/session/workloop_waiting_test.go`.
- `spec.md` (§6.2 and §9.1) documents `not_before` and the capped-waiter reporting behavior.

## Outcome

Added RFC 3339 not_before to task tools, eligibility and work-loop outcomes. Deferring a focused task counts as progress; a loop with only deferred work finishes and names the earliest check-in. Shell-idle foreground slots at the cap report waiting without detaching, including in restored digests and client stop confirmations. Verified with go test ./..., repeated race tests, web build/tests, macOS Swift tests and an iOS simulator build and dialog check.

Commit: Defer scheduled backlog work and report capped foreground waits accurately
