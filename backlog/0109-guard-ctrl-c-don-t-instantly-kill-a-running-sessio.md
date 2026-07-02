---
id: "0109"
title: 'Guard ctrl+c: don''t instantly kill a running session on a one-shot daemon'
status: done
priority: 3
created: "2026-07-01"
updated: "2026-07-02"
depends_on: []
spec_refs:
    - 3.1 Daemon lifecycle & projects
    - 18.2 Settings overlay
---

## Description
## Description
`ctrl+c` returns `tea.Quit` from every state. With the default one-shot in-process daemon, quitting tears down the daemon and any in-flight agent work — possibly mid-commit or mid-write. One accidental keypress ends everything with no confirmation, while the settings overlay deliberately made *leaving a session* an intentional act.

Add a lightweight guard when (a) the daemon is one-shot AND (b) a session is running/paused/pending-question: first ctrl+c shows "agent running — ctrl+c again (within ~2s) to quit"; second one quits. Attached to a persistent daemon (work survives), quit stays immediate.

## Acceptance criteria
- [ ] Double-press (or equivalent) required to quit while a one-shot session is running
- [ ] Immediate quit preserved when idle, on the home menu with no live session, or on a persistent daemon
- [ ] The guard message is visible where the user is looking (status/footer)
- [ ] Overlay "quit" row uses the same guard

## Acceptance criteria

## Plan

Goal: two-step ctrl+c guard in the TUI so an accidental keypress can't tear down a one-shot in-process daemon mid-work.

Context: `tui.Run(..., showPicker)` receives `persistent` from main.go — so `!m.showPicker` == one-shot daemon. `m.status` holds "running" / "paused" / "waiting for your answer" / "idle" / "error" / "stream closed". Every quit path today is a bare `return m, tea.Quit` scattered across ~18 `case "ctrl+c"` handlers plus the settings overlay's `ovQuit` row.

Design:
1. Model state: add `quitArmed bool` (or `quitArmedAt time.Time`) and `quitSeq int` (timer guard, mirroring the existing flashSeq pattern). Const `quitGuardWindow = 2 * time.Second`.
2. Helpers on model:
   - `quitGuardActive() bool`: false when `m.showPicker` (persistent daemon — work survives the client). Otherwise true when live agent work would be killed: `m.sessionID != "" && status in {running, paused, waiting for your answer}`, or `m.looping`, or `len(m.waitingSessions) > 0` (a background session pending a question on the same one-shot daemon), or `m.captureBusy` (server-side capture agent in flight).
   - `confirmQuit() (tea.Model, tea.Cmd)`: if guard inactive → `tea.Quit`. If already armed → `tea.Quit`. Else arm, bump quitSeq, and return a `tea.Tick(quitGuardWindow, ...)` cmd producing a `quitDisarmMsg{seq}`; Update handles that msg by disarming only when seq matches (stale-timer safety).
3. Wire-up: replace every `return m, tea.Quit` in key handlers with `return m.confirmQuit()` — session, menu, picker, history (list + transcript), capture, backlog (list + detail), plans, cost, digest, browse, model-backends (list/form/confirm), settings overlay ctrl+c, and the overlay `ovQuit` row (acceptance criterion). Exception: keep the fatal-error screen (`m.err != nil`) as an immediate quit — the UI is unusable there and any status is stale.
4. Visibility of the warning ("agent running — ctrl+c again to quit"):
   - session status bar (`statusBar()`): high-priority warning segment when armed (styled, e.g. errStyle/recoStyle ⚠).
   - `menuView()`: a notice line like the existing flashErr line.
   - shared `footerBar()`: when armed, replace/prefix the hint text with the warning so picker/history/backlog/etc. show it; likewise the `modalCard` hint used by modal overlays (capture/browse/overlay/etc.) — pick whichever chokepoints cover the settings overlay + modals without touching every view individually.
5. Tests (internal/tui/tui_test.go, model-level, using existing keyMsg helper):
   - one-shot + running session: first ctrl+c → no quit cmd, armed, warning visible in rendered view; second ctrl+c → cmd yields tea.QuitMsg.
   - one-shot + idle status → immediate quit.
   - persistent (showPicker=true) + running → immediate quit.
   - menu with no live session (one-shot) → immediate quit.
   - settings overlay: enter on quit row is guarded the same way while running.
   - disarm: quitDisarmMsg with matching seq clears the armed state (subsequent ctrl+c re-arms rather than quitting).
6. Verify: go build ./... && go test ./internal/tui/... (then go test ./... for safety).

### Starting points
- internal/tui/tui.go — all ctrl+c handlers: rg -n 'ctrl\+c' internal/tui/tui.go
- m.showPicker == persistent daemon (see tui.Run signature line ~313 and cmd/ycc/main.go:171)
- overlayActivate() case ovQuit (~line 3870) — must use the same guard
- flashErr/flashSeq (~lines 210-216, 534-544, 1879) — existing pattern for a self-clearing transient notice with a seq-guarded tea.Tick
- statusBar() segments ~line 5455; menuView() flashErr notice ~line 5203; footerBar() ~line 5063; modalCard() ~line 5094
- status values set in the event switch ~lines 4819-4853: running/paused/idle/error/'waiting for your answer'
- tests: internal/tui/tui_test.go keyMsg() helper ~line 1164

## Work log
- 2026-07-02 plan: Goal: two-step ctrl+c guard in the TUI so an accidental keypress can't tear down a one-shot in-process daemon mid-work.  Context: `tui.Run(..., showPicker)` receives `persistent` from main.go — so `
…[truncated]
- 2026-07-02 context hints: 7 recorded with plan
- 2026-07-02 context hints: internal/tui/tui.go — all ctrl+c handlers: rg -n 'ctrl\+c' internal/tui/tui.go; m.showPicker == persistent daemon (tui.Run line ~313; cmd/ycc/main.go:171); overlayActivate() case ovQuit ~line 3870; 
…[truncated]
- 2026-07-02 implementer report: Implemented task 0109: a two-step ctrl+c guard in the TUI so an accidental keypress can't tear down a one-shot in-process daemon mid-work.  Changes (internal/tui/tui.go): - Added `quitGuardWindow = 2 
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The change correctly implements a two-step ctrl+c guard for one-shot daemons. quitGuardActive() gates the guard to live one-shot work (running/paused/pending session, loop, waiting background sessions
…[truncated]
- 2026-07-02 decision: accept — commit: tui: two-step ctrl+c guard so an accidental keypress can't kill a one-shot daemon mid-work (0109)
