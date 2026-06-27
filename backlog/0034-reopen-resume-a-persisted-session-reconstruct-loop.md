---
id: "0034"
title: Reopen/resume a persisted session (reconstruct loop history)
status: todo
priority: 3
created: "2026-06-27"
updated: "2026-06-27"
depends_on:
    - "0033"
spec_refs: []
---

## Description
Implement "resume = replay" (spec §4.5/§5/§18.6): reopening a persisted session
re-instantiates its coordinator on the EXISTING event log instead of starting a fresh one,
so the human can re-enter a finished/idle session and continue it, with new activity
appended to the same `events.jsonl`.

## Context
- The live agent loop (`internal/engine/loop.go`) maintains `history []gollama.Message`
  (model turns with thinking/tool-call content, tool results, user inputs). Reopen must
  rebuild this history from the event log so the model continues coherently.
- `internal/session/session.go` `Manager` currently only creates new sessions; it needs a
  load-from-disk path that re-opens the log (`event.OpenLog` already replays into memory),
  restores mode + focus from `Reduce`, and registers the session so Subscribe/SendInput/
  AnswerQuestion work again.
- Relates to lifecycle/GC (task 0009) and context-window management (task 0010): a resumed
  long session may need budgeting before its first new turn.

## Acceptance criteria
- [ ] A reducer/replayer reconstructs the engine loop `history` from a session's events
      losslessly (model messages incl. thinking + tool calls, tool results, user inputs).
      If any required content is not currently captured in the log, FIX the emit side so it
      is — the log is meant to be the whole state (§5.1).
- [ ] `Manager` gains a load/reopen path: given a session id with a persisted log, re-open
      the log, restore mode + focus, instantiate the coordinator with the reconstructed
      history, and register it as a live session.
- [ ] `ResumeSession(session_id)` RPC (a.k.a. reopen) added to `SessionService`; idempotent
      if the session is already live (returns the live one).
- [ ] After reopen, `Subscribe` replays the full log and live turns append to the same
      `events.jsonl` (one continuous log, monotonic seq).
- [ ] Tests: round-trip a recorded session → reopen → history matches; a new turn continues
      and appends; reopening an already-live session is a no-op.

## Acceptance criteria

## Work log
