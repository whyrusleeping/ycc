---
id: "0072"
title: 'Work-loop mode: auto-advance past a finished (idle) work session'
status: done
priority: 2
created: "2026-06-30"
updated: "2026-06-30"
depends_on: []
spec_refs: []
---

## Description
## Description
In `work (loop)` mode the TUI starts a fresh `work` session per ready backlog task and
advances when the current session ends (spec §9). The loop driver (`loopNext`) was only
triggered by `streamClosedMsg`, i.e. when the session's event stream closes. But a `work`
session never closes itself after finishing: when the coordinator calls `finish`, the daemon
emits `session_idle` and then blocks waiting for user input, so the stream stays open and the
loop stalls at "session idle".

The loop is a client concern, so the client now drives the transition explicitly.

## Changes (all in `internal/tui/tui.go`)
- Add `loopStopping bool` to the model — guards the idle→stop transition so only one
  `StopSession` is issued per finished session.
- Add a `stopSession()` command that calls the `StopSession` RPC to hard-terminate the idle
  session, closing its event stream.
- In the `evMsg` drain handler, when `m.looping && !m.loopStopping && m.status == "idle"`,
  set `loopStopping`, show a "task finished — advancing…" status, and fire `stopSession()`.
  Closing the stream produces the existing `streamClosedMsg` → `loopNext()` → next task,
  reusing the existing loop-advance path (including the no-progress guard).
- Reset `loopStopping` on `startedMsg` (new session start).

Only happens while looping; a normal (non-loop) work session still goes idle and stays
usable for steering.

## Acceptance criteria
- [x] A finished (idle) session in loop mode is stopped automatically so the loop advances
      to the next ready task.
- [x] Only one `StopSession` is issued per idle session (guarded against repeat idle events).
- [x] A non-loop work session that goes idle stays put (remains usable).
- [x] Tests cover loop idle-advance and non-loop idle-stays-put; `go build ./...` and
      `go test ./internal/tui/...` pass.

## Work log
- Implemented (with tests) by a prior session but left uncommitted in the working tree;
  captured here so it can be reviewed and committed cleanly.

## Acceptance criteria

## Work log
- 2026-06-30 plan: The fix already exists in the working tree (from a prior uncommitted session): loopStopping guard + stopSession() command + idle-while-looping detection in the evMsg handler + reset on startedMsg, plu
…[truncated]
- 2026-06-30 review tier: single-opus — reviewers: Claude
- 2026-06-30 review (Claude): revise — The change adds the loopStopping guard, stopSession() command, idle-while-looping detection, and the startedMsg reset, and it builds and passes the (unit) tests. However, the core mechanism is broken 
…[truncated]
- 2026-06-30 implementer report: Fixed the work-loop idle-advance blocker and strengthened the test.  Changes (internal/tui/tui.go): - In the evMsg drain handler's idle-while-looping branch (line ~1043), changed the returned batch fr
…[truncated]
- 2026-06-30 review (Claude): accept — The revision addresses the blocker: the loop-idle branch now includes waitEvent(m.events) in the batch alongside stopSession(), so the single-reader invariant is preserved and the StopSession-induced 
…[truncated]
- 2026-06-30 decision: accept — commit: Work-loop mode: auto-advance past a finished (idle) work session (task 0072)  A work session does not self-terminate after finishing: the daemon emits session_idle and blocks for input, so its event s
…[truncated]
- 2026-06-30 usage: 33,163 tok (in 114, out 33,049, cache_r 1,108,895, cache_w 87,459) · cost n/a (unpriced)
