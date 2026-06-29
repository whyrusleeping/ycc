---
id: "0034"
title: Reopen/resume a persisted session (reconstruct loop history)
status: done
priority: 3
created: "2026-06-27"
updated: "2026-06-29"
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
- 2026-06-29 plan: Implement "resume = replay": reconstruct a coordinator loop's conversation history from a session's event log and reopen it as a live session.  1. event package (capture-side fix + new types):    - Ad
…[truncated]
- 2026-06-29 implementer report: Implemented "resume = replay" (task 0034): reopening a persisted session re-instantiates its coordinator on the EXISTING event log with reconstructed history, appending new activity to the same `event
…[truncated]
- 2026-06-29 review tier: single-opus — reviewers: Claude
- 2026-06-29 review (Claude): accept — The change correctly and completely implements "resume = replay" for task 0034. It adds engine.ReplayHistory to reconstruct the coordinator loop history (user inputs, assistant turns with thinking blo
…[truncated]
- 2026-06-29 revision: Addressed all three review fixes:  1. **Check-then-register race in Manager.Reopen** — Kept the early `m.Get(id)` fast-path, and after building the session/loop, registration is now atomic: under `m
…[truncated]
- 2026-06-29 review (Claude): accept — The revision cleanly resolves all three findings from the prior review. Reopen now registers atomically under the manager lock (loser cancels and closes its log, eliminating the check-then-act race an
…[truncated]
- 2026-06-29 decision: accept — commit 3c6bcf0: Reopen/resume a persisted session (reconstruct loop history)  Implement "resume = replay" (spec §4.5/§5/§18.6): reopening a persisted session re-instantiates its coordinator on the EXISTING event l
…[truncated]
