---
id: "0127"
title: Add affordance to return to the main menu from a finished work session
status: done
priority: 3
created: "2026-07-03"
updated: "2026-07-03"
depends_on: []
spec_refs: []
---

## Description
## Description

When a work session finishes (the agent goes idle / the session completes), there is no easy or obvious way to get back to the TUI main menu cleanly. Today the user is left in the session view with no clear "you're done — go back" affordance, and the paths out are unclear (quit entirely, or opaque key presses).

Add explicit affordances in the session view once a session is finished/idle, e.g.:

- A visible hint in the status bar / footer (e.g. "session finished — press esc/q to return to menu").
- A key binding that stops/detaches the finished session and returns to `stateMenu`, ensuring the session is stopped or persisted cleanly (stream closed, no orphaned daemon session).
- Make sure this interacts sensibly with the "work (loop)" driver (which already stops idle sessions itself) and with the ctrl+c quit guard.

## Acceptance Criteria

- [ ] When a session reaches a finished/idle state, the UI shows a clear hint for returning to the main menu.
- [ ] A single key press returns the user to the main menu from a finished session, cleanly closing/stopping the session (no dangling stream or orphaned session).
- [ ] Behavior does not break the work-loop driver's own idle→stop handling.
- [ ] Help overlay (help.go) documents the new binding.

## Acceptance criteria

## Plan

Goal: give a finished (idle / stream-closed) work session a clear, single-keypress path back to the main menu, cleanly stopping the session, without disturbing the work-loop driver.

Design (all in internal/tui/tui.go + help.go):

1. `sessionFinished()` helper on model:
   `m.state == stateSession && !m.looping && (m.status == "idle" || m.status == "stream closed")`.
   - "idle" = agent went idle after finishing (session_idle event); it blocks in the daemon waiting for input, so leaving must StopSession to avoid an orphan.
   - "stream closed" = the event stream already ended; nothing to stop.
   - Excludes looping sessions: the loop driver already auto-stops idle sessions and advances (existing evMsg handler, loopStopping guard) — this binding must not interfere.
   - Deliberately excludes "error"/"paused" (recoverable states; esc→overlay→"back home" remains the escape hatch there).

2. Key binding `q` in updateSession's main key switch, gated like `/` and `n`: only fires when `m.sessionFinished() && strings.TrimSpace(m.input.Value()) == ""`; otherwise fall through so `q` still types into the textarea. On trigger:
   - build `stop := m.stopSession()` FIRST (it captures m.sessionID) but only include it when `m.status == "idle"` (StopSession on an already-gone session returns NotFound → needless error flash);
   - clear transient session-input state (pending/picker/wizard, clearSearch, selected=-1), set `m.sessionID = ""`, `m.status = ""`, `m.state = stateMenu`;
   - return `tea.Batch(stop?, m.refreshMenu())`. A late streamClosedMsg from the stopped stream is harmless in the menu (non-loop handler just sets m.status).

3. Visible hint: in sessionView, when `m.sessionFinished()`, the help footer leads with a finished notice, e.g. `" ✔ session finished — q return to menu · ? help · enter expand · ↑↓ select · pgup/pgdn scroll · esc settings"`. This variant takes precedence over the work-mode loop-toggle footers (a finished non-loop work session shows the finished hint). Status bar already shows idle/stream-closed state — no change needed there.

4. help.go: add a `q` row to the "session" helpSection per the maintenance contract (e.g. "return to the menu when the session has finished (input empty; stops the session cleanly)").

5. Tests (internal/tui/tui_test.go, using the existing fake-client pattern):
   - status "idle", stateSession, not looping, empty input: `q` → stateMenu, StopSession issued exactly once for the session id.
   - status "stream closed": `q` → stateMenu, NO StopSession call.
   - status "running": `q` types into the input, state unchanged, no StopSession.
   - finished but input non-empty: `q` types.
   - m.looping with status "idle": `q` does not leave the session (loop driver owns the transition).
   - sessionView contains the finished hint when finished, and not while running.
   Run/refresh snapshot tests if the footer change affects them.

6. Verify: gofmt, go build ./..., go test ./... .

### Starting points
- internal/tui/tui.go:3122 updateSession — main key switch; see '/' and 'n' cases for the empty-input gating + fallthrough pattern
- internal/tui/tui.go:788 stopSession (captures m.sessionID at cmd build time)
- internal/tui/tui.go:2220 streamClosedMsg handler + 2287 loop idle→stop guard (do not disturb)
- internal/tui/tui.go:6443 'session_idle' → m.status = "idle"
- internal/tui/tui.go:7501 sessionView footer variants (work-mode loop hints at 7537-7546)
- internal/tui/tui.go:5108 ovBackHome — existing overlay route back to menu (leave as is)
- internal/tui/help.go helpSections 'session' block — maintenance contract requires updating it
- internal/tui/tui_test.go:4127 fake client construction pattern for RPC-recording tests

## Work log
- 2026-07-03 plan: Goal: give a finished (idle / stream-closed) work session a clear, single-keypress path back to the main menu, cleanly stopping the session, without disturbing the work-loop driver.  Design (all in in
…[truncated]
- 2026-07-03 context hints: 8 recorded with plan
- 2026-07-03 context hints: internal/tui/tui.go:3122 updateSession — main key switch; '/' (3313) and 'n' (3323) show the empty-input gating + fallthrough pattern; internal/tui/tui.go:788 stopSession — captures m.sessionID wh
…[truncated]
- 2026-07-03 implementer report: Implemented task 0127: a clean single-key path back to the main menu from a finished work session.  Changes (internal/tui/tui.go): - Added `sessionFinished()` helper: `m.state == stateSession && !m.lo
…[truncated]
- 2026-07-03 review tier: single-opus — reviewers: Claude
- 2026-07-03 review (Claude): accept — The change cleanly implements a single-key (`q`) path back to the main menu from a finished (idle / stream-closed) work session. It adds a well-documented `sessionFinished()` helper, a properly gated 
…[truncated]
- 2026-07-03 decision: accept — commit: tui: add `q` to return to the menu from a finished session (task 0127); block task 0120 (gollama repo unavailable)
