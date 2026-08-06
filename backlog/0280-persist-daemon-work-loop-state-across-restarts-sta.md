---
id: "0280"
title: Persist daemon work-loop state across restarts (state + digest, interrupted outcome)
status: todo
priority: 3
created: "2026-08-06"
updated: "2026-08-06"
depends_on: []
spec_refs:
    - 9. Modes (the home menu)
---

## Description
The daemon-side work loop (task 0179) keeps all loop state in memory: `Manager.workLoops` is a plain map, so a daemon restart silently loses a running loop AND the finished batch digest. `GetWorkLoop` then returns nil, and clients (iOS task 0190, TUI task 0267) show "no work loop yet" — with no signal that a drain was in flight and died.

## Scope
- Persist loop state per workspace (alongside the existing session/event persistence, e.g. `.ycc/` state) — at minimum: loop id, project, state, started_at, current session id, per-session records, accumulated tokens/cost/status, and the finished digest.
- On daemon start, restore the last snapshot for each workspace so `GetWorkLoop` can answer after a restart.
- Decide and implement the restart semantics for a loop that was `running`/`stopping` when the daemon died: do NOT silently resume unattended spend — mark it terminated with an explicit outcome (e.g. "loop interrupted: daemon restarted") so clients render an accurate end state, and let the user start a new loop.
- Make sure a restored/terminated loop does not block `StartWorkLoop` (the already-running precondition must only reject a genuinely live loop).

## Acceptance criteria
- A finished loop's digest survives a daemon restart and is still returned by `GetWorkLoop`.
- A loop that was running when the daemon stopped is reported with a clear interrupted outcome rather than vanishing or resuming on its own.
- `StartWorkLoop` succeeds after a restart (no phantom "already running").
- Unit tests cover persist → restart → restore, including the interrupted case.

## Work log
