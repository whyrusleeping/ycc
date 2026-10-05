---
id: "0421"
title: Show idle-with-live-subagents as active (awaiting background jobs) across clients
status: in_review
priority: 2
created: "2026-10-04"
updated: "2026-10-04"
depends_on: []
spec_refs:
    - Background jobs
---

## Description
## Problem
When the coordinator returns a final response while a subagent / background job is still live, every client shows the session as plain **idle/finished** even though work continues and the coordinator will auto-resume when the job completes (0379).

The daemon already knows: `Session.StatusWithJobContinuation()` returns `(idle, awaiting=true)` and the `session_idle` event carries `awaiting_jobs: true` (session.go ~1674). The work loop, reaper, workstream-ready and memory-groom all honor it. **No client-facing surface does:**

- `sessionview/store.go applyEvent`: `SessionIdle → Phase="idle"` unconditionally (the indexed `SessionViewState.phase` iOS uses). Subagent-actor events are ignored for phase, so nothing flips it back until the coordinator wakes.
- `server.ListSessions` / `Manager.ListSessionHistory` live overlay use `s.Status()` → `"idle"`. iOS `SessionListModel.accumulate` does not count it as active; `lifecycleLabel` hides idle; `SessionReadStore` treats it as not-running.
- iOS `SessionProjection.foldPhase`: `session_idle → .idle`, subagent events ignored.
- TUI `transcript.go`: `session_idle → status "idle"`; it then flips to "running" on ANY actor's tool_call/model_turn (inconsistent flicker, not intended).
- **Hazard:** TUI `sessionFinished()` is true on "idle", so `q` calls StopSession → `Session.Stop` → `killJobs()` — kills the running subagent. Same for "loop armed: starts when this session finishes" (tui.go ~1348) starting a loop next to live delegated work.
- Notifications: `session_idle` always sends `KindIdle` ("finished") push even when awaiting jobs.

## Proposed design (backward compatible, no new status string)
- Proto: add `bool awaiting_jobs` to `SessionViewState`, `SessionInfo`, `SessionSummary` (older clients keep seeing "idle"). Regenerate Go + Swift (commit both, 0254).
- Live status: `awaiting = StatusWithJobContinuation().awaiting || hasLiveJobs()` (the reaper's rule) for ListSessions / ListSessionHistory live rows.
- Event-derived (sessionview index, iOS projection, TUI, persisted history reduction): `session_idle{awaiting_jobs:true}` sets idle+awaiting; cleared by the next coordinator activity/lifecycle event (resume turn, stop, error, interrupt, reopen). Persisted non-live rows: awaiting is false (jobs don't survive restart).
- TUI: header shows e.g. "● waiting on background jobs" (running-ish color/spinner); `sessionFinished()` false while awaiting (q must not kill the subagent; loop-arm waits); stop subagent-actor events from flipping coordinator status.
- iOS: projection/list/read-store treat awaiting as active (count in `active`, label "background"/"working", spinner in session chrome); user can still type (input while idle is fine).
- Notify: suppress or reword the idle push when `awaiting_jobs` (the real "finished" push comes from the post-wake session_idle).
- Spec: one sentence in the background-jobs section that clients present idle+awaiting as active, not finished.

## Acceptance
- Go tests: sessionview phase/awaiting folding; ListSessionHistory/ListSessions report awaiting for a live idle session with a live job and clear it after; TUI status + `sessionFinished` false while awaiting; no idle push while awaiting.
- iOS (on Mac): projection + SessionListModel tests for awaiting.


## Acceptance criteria

## Work log
