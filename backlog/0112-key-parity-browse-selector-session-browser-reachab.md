---
id: "0112"
title: 'Key parity: browse selector + session browser reachable from within a session'
status: done
priority: 4
created: "2026-07-01"
updated: "2026-07-02"
depends_on: []
spec_refs:
    - 18.6 Session history browser & reopen
---

## Description
## Description
Modal/browse chords are inconsistent between states: `ctrl+b` (backlog) and `ctrl+n` (capture) work in both menu and session, but `ctrl+o` (browse selector) and `ctrl+r` (session browser) work only on the home menu. From inside a session there is no route to the session browser, plans, or cost view except leaving via the overlay. Make the browse selector (and its targets) available globally.

## Acceptance criteria
- [ ] ctrl+o opens the browse selector from a session; targets (backlog/plans/sessions/cost) work and return to the session on esc
- [ ] Session-history browsing from within a session is read-only (no accidental reopen-over-live-session footguns; reopen may be disabled there)
- [ ] Session footer/help updated

## Acceptance criteria

## Plan

Goal: make the browse selector (ctrl+o) and the session browser reachable from inside a live session, with session-history browsing there being read-only (no reopen-over-live-session footgun), and update footer/help.

Current state (internal/tui/tui.go):
- ctrl+o/ctrl+r are handled only in updateMenu. The browse selector (m.browse) and its targets backlog/plans/cost/workstreams/digest are already modal flags that work over any state — only the "sessions" target switches m.state to stateHistory (a full state, not a modal), and its transcript drill-in clobbers the LIVE session's event pipeline (m.evs, m.bodyCache, m.deliveredSeqs, m.vp) via the transcriptMsg handler.

Design: add a modal variant of the session browser used when opened from a session.

1. New model fields: `histModal bool` (session browser open as a modal over the live session), `histModalTranscript bool`, `histModalID string` (session id of the open transcript), `histModalVP viewport.Model` (separate scroll viewport for the modal transcript so the live session's m.vp/m.evs are untouched).

2. Keybindings in updateSession:
   - main key switch: `ctrl+o` → m.openBrowse(); `ctrl+r` → open the history modal directly (histModal=true, historyCursor=0, history=nil, historyMsgTxt="loading…", return m.fetchHistory).
   - m.picking branch: add `ctrl+o` (parity with the existing ctrl+b there) and `ctrl+r` the same way.

3. updateBrowse "sessions" case: if m.state == stateSession, open the modal variant (as above) instead of switching to stateHistory. From the menu, behavior is unchanged.

4. Update routing: in Update, alongside the other modal checks (capture/backlog/plans/cost/ws/digest), add `if m.histModal { return m.updateHistoryModal(msg) }`. updateHistoryModal:
   - transcript open: esc/q/backspace/left → back to list (clear histModalTranscript/histModalID); ctrl+c → confirmQuit; everything else scrolls m.histModalVP. NO `o`/enter reopen.
   - list: esc/q → close the modal (histModal=false), returning to the live session; r → refresh (m.fetchHistory — the shared historyMsg handler already just fills m.history); up/down → navUp/navDown on m.historyCursor; enter → m.fetchTranscript(sel.SessionId); NO `o` reopen. ctrl+c → confirmQuit.

5. transcriptMsg handler: when m.histModal, do NOT touch m.evs etc. Instead render the replayed events statelessly and put the string into m.histModalVP, set histModalTranscript=true, histModalID=msg.id. Stateless render helper, e.g. `func (m model) renderTranscriptContent(events []*v1.Event) string`: make a scratch copy of the model value (scratch := m), give it fresh evs/expanded/bodyCache/deliveredSeqs (deliveredSeqSet(events))/eventStart/selected=-1/follow=false, then loop i over events skipping scratch.hiddenRow(i) and concatenating scratch.renderBlock(i, ev) — same pipeline as rebuild() but writing to a string, never mutating the live model (maps must be fresh in the scratch so shared caches aren't polluted). Size m.histModalVP like refreshBacklogDetailVP does (create on first use, SetWidth/SetHeight from m.w/m.h; also refresh its size on WindowSizeMsg like backlogVP).

6. Rendering: in render(), add `if m.histModal { ... }` with the other modal flags: transcript open → a read-only transcript view (title bar " ycc — transcript · <title> ", m.histModalVP.View(), footer " ↑↓/pgup/pgdn scroll · esc/q back · read-only"); otherwise the session list via the shared browser card (reuse/factor historyView's row building; hint "↑/↓ choose · enter transcript · r refresh · esc/q back" — no `o reopen`, this variant is read-only).

7. Footer/help:
   - session footer (~line 6314) gains "ctrl+o browse" (keep it terse; it's width-clamped).
   - internal/tui/help.go: global section — update ctrl+r desc ("previous sessions" — no longer menu-only); session browser section — note that `o` reopen works from the menu only / browsing from a session is read-only; session section stays accurate.

8. Tests (internal/tui/tui_test.go, mirroring TestBrowseMenuRoutes / TestSessionBrowserTranscript):
   - ctrl+o inside a session opens the browse selector; esc returns to the session view (state still stateSession, m.evs untouched).
   - browse → sessions from a session sets histModal (not stateHistory); ctrl+r from session likewise.
   - enter on a row loads the transcript into the modal (histModalTranscript) and does NOT clobber m.evs/m.deliveredSeqs of the live session.
   - `o` in the modal list and transcript does nothing (no ResumeSession RPC recorded by the fake).
   - esc unwinds: transcript → list → session.

Verify: gofmt, go build ./..., go test ./internal/tui/ (and full go test ./...).

### Starting points
- internal/tui/tui.go:2650-2900 — updateMenu/updateSession key handling (ctrl+o/ctrl+r in menu only today)
- internal/tui/tui.go:2426-2505 — Update modal routing order
- internal/tui/tui.go:3265-3355 — browseTargets/openBrowse/updateBrowse/browseView
- internal/tui/tui.go:2557-2634 — updateHistory (full-state variant, keep unchanged)
- internal/tui/tui.go:2061-2084 — transcriptMsg handler (clobbers m.evs — must branch for modal)
- internal/tui/tui.go:5679-5706 — rebuild(); renderBlock at 6438; hiddenRow at 5622; deliveredSeqSet
- internal/tui/tui.go:4211-4240 — refreshBacklogDetailVP pattern for a secondary viewport
- internal/tui/tui.go:5809-5856 — render() modal dispatch; historyView/transcriptView at 5959-6035
- internal/tui/help.go — helpSections (global + session browser sections)
- internal/tui/tui_test.go:2061-2230 — TestPreviousSessionsReopen / TestSessionBrowserTranscript / TestBrowseMenuRoutes patterns

## Work log
- 2026-07-02 plan: Goal: make the browse selector (ctrl+o) and the session browser reachable from inside a live session, with session-history browsing there being read-only (no reopen-over-live-session footgun), and upd
…[truncated]
- 2026-07-02 context hints: 10 recorded with plan
- 2026-07-02 context hints: internal/tui/tui.go:2650-2900 — updateMenu/updateSession key handling (ctrl+o/ctrl+r in menu only today); internal/tui/tui.go:2426-2505 — Update modal routing order; internal/tui/tui.go:3265-3355 
…[truncated]
- 2026-07-02 implementer report: Implemented Task 0112: browse selector + session browser reachable from within a live session, with read-only history browsing there.  ## Changes (internal/tui/tui.go) - New model fields: `histModal`,
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The change satisfies all acceptance criteria for task 0112. ctrl+o (browse selector) and ctrl+r (session browser) are now reachable from within a live session; the session-history variant is a strictl
…[truncated]
- 2026-07-02 decision: accept — commit: tui: make browse selector + read-only session browser reachable from within a session (task 0112)
