---
id: "0460"
title: 'Work loop: stop after consecutive no-progress sessions'
status: proposed
priority: 2
created: "2026-10-12"
updated: "2026-10-12"
depends_on: []
spec_refs: []
---

## Description
**Evidence (vals loop_b429b003, 2026-10-11):** 5 consecutive work sessions (s_6fd7c437, s_82edb356, s_a75c10ea, s_3167f16a, s_d9167852; 20–130 s each) selected the same "ready" tasks and ended with no commit and no status change. `decideLoop` (internal/session/workloop.go) deliberately continues whenever a ready task exists ("regardless of unchanged backlog metadata"), so the loop only stopped because a later session manually parked the tasks as blocked.

**Proposal:** track per-session progress (commit_made, task status/dependency changes, new tasks) and stop the loop after N (e.g. 2) consecutive sessions without progress. Use a distinct outcome such as "loop stalled: N sessions made no progress on <ids>", and say so in the digest/notification so the owner sees why it stopped.

**Acceptance:**
- `decideLoop` unit tests cover the stall stop, and show a session with a commit or status change resetting the counter.
- The digest names the stalled task ids and the last session's stated reason.

## Acceptance criteria

## Work log
