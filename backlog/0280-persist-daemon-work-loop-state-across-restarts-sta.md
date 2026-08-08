---
id: "0280"
title: Persist daemon work-loop state across restarts (state + digest, interrupted outcome)
status: done
priority: 3
created: "2026-08-06"
updated: "2026-08-08"
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

## Plan

Persist daemon work-loop state per workspace so a restart doesn't silently lose a running loop or a finished digest.

Design:
1. New file internal/session/workloop_persist.go with a JSON-serializable snapshot type (mirrors WorkLoop: loop id, project label, workspace, state, current session id, outcome, started_at, per-session records [id/focus/tokens/cost/price_status], digest task lists [completed/blocked/in_review/created], cumulative tokens/cost/cost-status). Include a schema/version field.
2. Persist location: `<workspace>/.ycc/workloop.json` (alongside existing `.ycc/sessions` persistence). Write atomically (temp file + rename), best-effort (log/ignore errors — persistence must never break a loop).
3. Persist points (all from the workLoop, under wl.mu snapshot semantics): on StartWorkLoop (state=running), after each accumulate (session record folded in), on StopWorkLoop (state=stopping), and in finish (state=finished with digest + outcome). This ensures the on-disk file says "running"/"stopping" iff a live loop died with the daemon.
4. Restore lazily: in Manager.GetWorkLoop and the StartWorkLoop already-running check, when m.workLoops[absWS] is nil, attempt to load `<workspace>/.ycc/workloop.json` (helper `loadWorkLoop`). Restore it as a non-live, finished workLoop stored in m.workLoops:
   - If the persisted state was "finished": restore as-is (digest survives restart).
   - If it was "running" or "stopping": mark state=finished, outcome = `loop interrupted: daemon restarted` (append original outcome context if any), clear currentSessionID, and persist the fixed-up snapshot back so the interruption is durable.
   Restored loops never get a goroutine and never resume — StartWorkLoop's existing precondition (only reject running/stopping) means a restored loop never blocks a new start.
   Do the restore under loopMu so concurrent Get/Start don't double-load.
5. Conversion: restored per-session records become loopSessRec (commits/verdicts are only inputs to digest building, which is already done for a finished loop, so they may be dropped); digest fields restore directly into wl.completed/blocked/inReview/created. Guard finish/buildDigest so a restored loop's digest isn't rebuilt/clobbered (restored loops never run, so finish is never called on them — but keep snapshot() faithful).
6. Tests in internal/session/workloop_test.go (or workloop_persist_test.go):
   - finished loop persists; a fresh Manager over the same workspace returns the digest from GetWorkLoop (sessions, totals, completed/blocked lists, outcome intact).
   - persisted running loop → fresh Manager reports state finished with an interrupted outcome, and the file is rewritten to finished.
   - StartWorkLoop succeeds on a fresh Manager after restoring an interrupted/finished loop (no phantom "already running"); use the existing newRunSession test seam.
   - corrupt/absent file → GetWorkLoop returns nil loop without error.
7. Run gofmt, go build ./..., go test ./internal/session/ (note: internal/session has known flaky tests unrelated to this — compare against HEAD if something odd fails).

No proto/RPC changes needed: GetWorkLoop already returns the full snapshot; clients just start seeing answers after restarts.

### Starting points
- internal/session/workloop.go — workLoop struct, snapshot(), StartWorkLoop/StopWorkLoop/GetWorkLoop, finish()
- internal/session/session.go:1350-1465 — Manager struct (loopMu, workLoops), NewManager
- persist path precedent: filepath.Join(absWS, ".ycc", "sessions", id, "events.jsonl") in session.go
- test seam: Manager.newRunSession func(*workLoop) func(ctx) (loopSessRec, bool, error); existing loop tests at internal/session/workloop_test.go:180+ use NewManager(reg, ws)

## Work log
- 2026-08-08 plan: Persist daemon work-loop state per workspace so a restart doesn't silently lose a running loop or a finished digest.  Design: 1. New file internal/session/workloop_persist.go with a JSON-serializable 
…[truncated]
- 2026-08-08 context hints: 4 recorded with plan
- 2026-08-08 context hints: internal/session/workloop.go — workLoop struct, snapshot(), StartWorkLoop/StopWorkLoop/GetWorkLoop, accumulate, finish; internal/session/session.go:1350-1420 — Manager struct (loopMu, workLoops ma
…[truncated]
- 2026-08-08 preload: 3 file(s), ~36 KiB seeded into implementer context
- 2026-08-08 implementer report: Implemented Task 0280 work-loop persistence.  Changes: - Added versioned JSON snapshots at `<workspace>/.ycc/workloop.json`, written best-effort via atomic temp-file rename. - Persisted loop identity/
…[truncated]
- 2026-08-08 review tier: single-opus — reviewers: sol
- 2026-08-08 review (sol): accept — The change satisfies Task 0280. It adds versioned, atomic per-workspace work-loop snapshots; persists running, stopping, per-session accumulation/current-session updates, and final digest/outcome stat
…[truncated]
