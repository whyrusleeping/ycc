---
id: "0119"
title: Search + jump navigation in the histModal transcript (session browser over a live session)
status: done
priority: 5
created: "2026-07-02"
updated: "2026-07-02"
depends_on:
    - "0116"
spec_refs:
    - 18.6 Session history browser & reopen
    - 18. Client UI (TUI)
---

## Description
## Description
Task 0116 added `/` incremental search (n/N, esc) and jump-to-event keys ({}()<>[]) to the live session view and the stateHistory transcript drill-in — the two surfaces sharing the m.evs/m.vp event pipeline. The session browser opened as a modal OVER a live session (ctrl+r from within a session, task 0112) renders transcripts into a separate plain-string viewport (histModalVP via renderTranscriptContent) and was explicitly out of scope, so its transcripts have no search or jump navigation.

Extend the same navigation to the histModal transcript. Since it doesn't use the event pipeline, this likely means either (a) line-based search over the rendered string content (scroll-to-line instead of selection), or (b) refactoring the modal to reuse the shared searchable-text helpers against msg.events kept alongside the viewport.

## Acceptance criteria
- [ ] `/` search with n/N next/prev and esc-clear works inside a histModal transcript
- [ ] Jump keys for question/review/commit/error events work (or a documented, deliberate subset if line-based search is chosen)
- [ ] The live session behind the modal is never disturbed (no m.evs/m.vp mutation)
- [ ] help.go "session browser" section updated per the maintenance contract

## Acceptance criteria

## Plan

Goal: add `/` search (incremental, n/N, esc-clear) and {}()<>[] jump-to-event navigation to the histModal transcript (session browser opened OVER a live session), without touching the live session's m.evs/m.vp/search state.

Approach: option (a) from the task — LINE-BASED navigation over the rendered string content, with event-start-line metadata recorded at render time so the jump keys work fully (no reduced subset needed). The shared event-pipeline helpers (searchStep/jumpToEvent) mutate m.selected/m.rebuild/m.vp, so they must NOT be reused here.

1. State (new model fields, fully separate from the live m.searching/m.searchQuery so an active live-session search is never clobbered):
   - histModalEvents []*v1.Event — the replayed events kept alongside the viewport
   - histModalLines []string — ansi-stripped rendered lines for case-insensitive matching
   - histModalEventLines — per visible event: start line + event type (for jumps)
   - histModalSearching bool, histModalQuery string, histModalCurLine int (-1 = none; the “cursor” line shared by search + jump)

2. Rendering: extend/refactor renderTranscriptContent (or add a sibling) so refreshHistModalVP also captures the plain lines and event start-line metadata. Store events in the model when the transcript loads (transcriptMsg → histModal branch) and reset all nav state there, when backing out to the list, and when closing the modal. On the current match/cursor line, re-set viewport content with that line highlighted (strip its ANSI, render reverse/selected style) and scroll it into view (SetYOffset, roughly centered). Since events are now retained, a window resize while the transcript is open should re-render via refreshHistModalVP-style logic (preserve or re-clamp scroll sensibly).

3. Keys in updateHistoryModal's histModalTranscript branch, mirroring updateHistory's transcript branch semantics:
   - While histModalSearching: printable chars append to query + incremental re-jump (first matching line at/after current line, wrapping); backspace edits; enter keeps the query active; esc cancels; ctrl+c quits.
   - `/` starts search; n/N cycle next/prev match with wraparound; esc with an active query clears it and stays in the transcript (second esc backs out to the list, as today).
   - { } ( ) < > [ ] jump backward/forward to the nearest question_asked / review_submitted / commit_made / session_error event start line, no wrap (same semantics as jumpToEvent).
   - Everything else still scrolls histModalVP.

4. Footer (histModalView transcript branch): while typing show a search bar (generalize searchBar or add a modal variant that renders histModalQuery + line-match counter); with an active query show ⌕ "q" k/N · n/N next/prev · esc clear; default hint becomes " ↑↓/pgup/pgdn scroll · / search · {}()<>[] jump · esc/q back · read-only".

5. help.go: the "session browser" section already documents / n/N and jump keys generically; verify wording now holds for both variants and adjust if anything is modal-specific (per the maintenance contract). Update the updateHistoryModal doc comment.

6. Tests (internal/tui/tui_test.go, following the existing histModal tests around line 2300): drive the modal open, inject transcriptMsg with representative events (incl. question_asked/review_submitted/commit_made/session_error), then verify: `/`+typing selects a matching line and scrolls; n/N wrap; esc clears query then second esc backs out; jump keys land on the right event lines; and the live session's m.evs, m.vp offset, m.searching/m.searchQuery are untouched throughout.

Verify: go build ./... && go test ./internal/tui/... (then go test ./...).

### Starting points
- internal/tui/tui.go:2793 updateHistoryModal (transcript branch = where new keys go)
- internal/tui/tui.go:4572 refreshHistModalVP + 4595 renderTranscriptContent (render pipeline to extend with line metadata)
- internal/tui/tui.go:2099 transcriptMsg handler (histModal branch loads the transcript)
- internal/tui/tui.go:5820-5944 task 0116 search/jump helpers — semantics to mirror, but they mutate m.evs/m.selected/m.vp; do NOT call them from the modal
- internal/tui/tui.go:6541 histModalView (footer/search-bar rendering) and 6932 searchBar
- internal/tui/help.go:100 'session browser' help section + maintenance contract at top
- internal/tui/tui.go:1955 window-resize handling of histModalVP
- internal/tui/tui_test.go:~2300 existing histModal tests (patterns for driving the modal)

## Work log
- 2026-07-02 plan: Goal: add `/` search (incremental, n/N, esc-clear) and {}()<>[] jump-to-event navigation to the histModal transcript (session browser opened OVER a live session), without touching the live session's m
…[truncated]
- 2026-07-02 context hints: 8 recorded with plan
- 2026-07-02 context hints: internal/tui/tui.go:2793 updateHistoryModal — transcript branch is where the new keys go; internal/tui/tui.go:4572 refreshHistModalVP, 4595 renderTranscriptContent — extend to capture lines + even
…[truncated]
- 2026-07-02 implementer report: Implemented Task 0119: line-based `/` search (n/N, esc-clear) and {}()<>[] jump-to-event navigation in the histModal transcript (session browser opened as a modal OVER a live session), without touchin
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The change adds line-based `/` search (incremental, n/N with wrap, esc-clear) and full {}()<>[] jump-to-event navigation to the histModal transcript using dedicated state (histModalEvents/Lines/EventL
…[truncated]
- 2026-07-02 decision: accept — commit: tui: line-based search (/ n/N esc) and {}()<>[] jump navigation in the histModal transcript (task 0119)
- 2026-07-02 usage: 47,468 tok (in 198, out 47,270, cache_r 4,469,368, cache_w 166,865) · cost n/a (unpriced)
  implementer: 34,973 tok (in 150, out 34,823, cache_r 3,742,120, cache_w 79,723) · cost n/a (unpriced)
  reviewer:Claude: 7,246 tok (in 24, out 7,222, cache_r 182,573, cache_w 31,262) · cost n/a (unpriced)
  coordinator: 5,249 tok (in 24, out 5,225, cache_r 544,675, cache_w 55,880) · cost n/a (unpriced)
