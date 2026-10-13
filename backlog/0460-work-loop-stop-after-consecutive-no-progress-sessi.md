---
id: "0460"
title: 'Work loop: back off, then stop, after consecutive no-progress sessions'
status: done
priority: 2
created: "2026-10-12"
updated: "2026-10-13"
depends_on: []
spec_refs:
    - 9.1 Unattended work loop
---

## Description
**Evidence (vals loop_b429b003, 2026-10-11):** 5 consecutive work sessions (s_6fd7c437, s_82edb356, s_a75c10ea, s_3167f16a, s_d9167852; 20–130 s each) selected the same "ready" tasks and ended with no commit and no status change. `decideLoop` (internal/session/workloop.go) deliberately continues whenever a ready task exists ("regardless of unchanged backlog metadata"), so the loop only stopped because a later session manually parked the tasks as blocked.

**Evidence (wheeler loop_f09e4f2c, 2026-10-12):** the three ready tasks (0025, 0017, 0012) were all gated on future times (archive download ~10-15, M1 window 10-13..10-20, post-M1). From 13:50 to 21:14 UTC the loop ran 1,807 back-to-back ~15 s sessions, each re-checking the gates and finishing "I did no work and changed nothing", until the owner stopped it. They used ~68M cache-read, 9.3M cache-write and 2.1M output tokens on Opus (the loop's own token total, which excludes cache, showed ~1k per session). Sessions considered `blocked` and declined it because a time gate is not an external blocker.

The 0344 lesson still applies: unchanged task *status* is not a stall (vals sessions committed failed-hardware evidence and returned the task to todo).

**Design:**
- A completed (non-errored) loop session made progress if it recorded a commit or changed any backlog file (status, body/work log, new or removed task). Memory notes and other workspace edits do not count.
- After a no-progress session the loop waits before the next one, escalating 2m, 5m, 15m, 30m, then hourly. The loop shows `waiting` with wait kind `no_progress` and a resume time; graceful stop wakes it immediately. Any progress resets the backoff.
- Once a stall has waited 12h in total, the loop stops: "loop stalled: N consecutive sessions made no progress (ready: <ids>); gave up after 12h". The finished digest keeps each session's report.
- Time-gated work therefore resumes on its own when a gate opens within the window, at a few sessions per hour instead of several per minute.

**Acceptance:**
- Tests: no-progress sessions wait on the escalating schedule; a commit or backlog change resets it; the stall stop fires after the patience with the stalled outcome; stop during a stall wait finishes "requested".
- Spec §9.1 and remote-api describe the backoff and stall stop; TUI/web/iOS waiting lines distinguish `no_progress` from provider waits.

## Acceptance criteria

## Work log
- 2026-10-13: Implemented in internal/session/workloop.go: progress = commit_made or a backlog-dir content hash change across the session; no-progress backoff applied after decideLoop (so budget/no-ready stops never wait, and the loop re-decides after waiting); provider waits share waitBeforeNext. Tests: backoff schedule + reset on work-log/commit, 12h stall stop (17 sessions, outcome names ready ids), re-decide after wait, 0344 regression test now asserts backoff instead of continuous restarts. Spec §9.1, remote-api, TUI/web/iOS waiting + envelope lines updated; web dist rebuilt. go test session/tui/server/web and -race loop tests pass; iOS YccKit change (string only) not built here.
