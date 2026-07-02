---
id: "0116"
title: Transcript search (/) and jump-to-event navigation
status: done
priority: 4
created: "2026-07-01"
updated: "2026-07-02"
depends_on: []
spec_refs:
    - 18.6 Session history browser & reopen
    - 18. Client UI (TUI)
---

## Description
## Description
Long work sessions produce thousands of events; the only navigation is ↑/↓ selection-walking and pgup/pgdn. Add transcript navigation for the live session view and the read-only transcript browser: `/` incremental search (highlight + n/N next/prev match), and jump keys for semantically interesting events (previous/next question, review verdict, commit, error). Search should match the rendered text (headlines + expanded bodies).

## Acceptance criteria
- [ ] `/` searches the transcript; n/N cycle matches; esc cancels back to normal keys
- [ ] Jump keys for question/review/commit/error events (documented in help/footer)
- [ ] Works in both the live session view and the history transcript view
- [ ] Search state doesn't interfere with the input textarea (only when input empty or via an explicit mode)

## Acceptance criteria

## Plan

Add transcript search and jump-to-event navigation to the two surfaces that share the event-rendering pipeline (m.evs/m.vp): the live session view (stateSession) and the history transcript drill-in (stateHistory + historyTranscript). The histModal transcript (plain-string viewport over a live session) is explicitly out of scope (follow-on task).

1. Model state (tui.go):
   - `searching bool` (typing the query in the footer), `searchQuery string` ("" ⇒ search inactive).
   - Do NOT store a match list: compute matches on demand by scanning m.evs, so appended live events can never leave stale indices.

2. Match semantics ("rendered text: headlines + expanded bodies"):
   - Helper `searchableText(i) string`: plain text of the event's rendered headline — ansi.Strip(detailLineFor(ev)) plus the type/actor labels — and, when the event is expanded (m.eventExpanded), ansi.Strip(bodyFor(ev)). Case-insensitive substring match against the query.
   - Skip hiddenRow(i) events (folded tool_results / empty model_turns) — they share the owning row; match on the owning visible row only (the folded result's text participates via the combined row when expanded, same as what's on screen).

3. Search flow (shared helper used by both updateSession and updateHistory's transcript branch):
   - `/` enters search-entry mode. In the live session only when the input textarea is trimmed-empty (same gating precedent as `?`); blur the input while searching. In the transcript view unconditional.
   - While `searching`: printable runes append to the query, backspace deletes a rune, each edit incrementally jumps selection to the first match at/after the current selection (wrapping); enter confirms (exit entry mode, query stays active for n/N); esc cancels entirely (clear query, refocus input in session view); ctrl+c still quits.
   - With a confirmed query: `n`/`N` jump to next/prev match (wrap around), gated on empty input in the session view. Selection moves to the match (m.selected, follow=false, rebuild + ensureVisible) — the existing selection bar is the highlight — and the footer shows a live `⌕ "query" k/N` counter.
   - esc with an active (confirmed) query in the session view clears the search INSTEAD of opening the settings overlay (intercept in the top-level esc handler); in the transcript view esc clears the search instead of going back to the list. Second esc behaves normally.

4. Jump keys (both surfaces; gated on empty input in the session view, unconditional in the transcript):
   - `{` / `}` prev/next question (question_asked)
   - `(` / `)` prev/next review verdict (review_submitted)
   - `<` / `>` prev/next commit (commit_made)
   - `[` / `]` prev/next error (session_error)
   Implemented via one helper `jumpToEvent(dir int, types ...string)`: scan from m.selected in dir for the nearest matching, non-hidden event; no-op when none. Sets selected, follow=false, rebuild, ensureVisible.

5. UI/layout:
   - While `searching`, the session view replaces the input row with a one-row search bar (` / query▌ · k/N · enter keep · esc cancel`); footerStackHeight must account for it (1 row instead of inputViewHeight) and relayout is called on mode enter/exit so the frame stays exact (TestSessionViewFitsTerminal must keep passing). With a confirmed query the input row returns and the footer help line carries the `⌕ "query" k/N · n/N · esc clear` hint.
   - Transcript view: while searching, the footer shows the query line; otherwise its hint gains `/ search` and the jump keys.
   - Reset search state whenever the pipeline resets: startedMsg, transcriptMsg load, and leaving the transcript back to the list.

6. Help catalog (help.go maintenance contract): add the new bindings to the "session" and "session browser" sections (search `/`, n/N, and the four jump-key pairs).

7. Tests (tui_test.go): searchable-text matching (case-insensitive, expanded body included, hidden rows skipped); `/`+typing jumps selection and n/N cycle with wrap; esc cancels back to normal keys and refocuses input; `/` and n/N do NOT hijack a non-empty input textarea; jump keys land on prev/next question/review/commit/error; transcript-branch search works and esc clears before backing out; layout stays within the terminal frame while searching.

Verify with go build ./... && go test ./... and gofmt.

### Starting points
- internal/tui/tui.go: updateSession ~L2899 (main key switch; note '?' empty-input gating precedent), updateHistory transcript branch ~L2599
- internal/tui/tui.go: top-level esc→openOverlay handler ~L2533 (intercept for search-clear in stateSession)
- internal/tui/tui.go: moveSelection/hiddenRow/eventExpanded/rebuild/ensureVisible ~L5594-5900; detailLineFor ~L7865; bodyFor ~L6933
- internal/tui/tui.go: sessionView ~L6533, transcriptView ~L6259, footerStackHeight/relayout ~L1795-1824, inputRow ~L1732
- internal/tui/tui.go: pipeline resets at transcriptMsg ~L2083 and startedMsg ~L2128
- internal/event/event.go: event types question_asked, review_submitted, commit_made, session_error
- internal/tui/help.go: helpSections maintenance contract — keep 'session' and 'session browser' sections in sync
- x/ansi already imported in tui.go (ansi.Truncate); ansi.Strip available for plain-text matching
- internal/tui/tui_test.go: TestSessionViewFitsTerminal ~L856 (layout invariant), model construction patterns for tests

## Work log
- 2026-07-02 plan: Add transcript search and jump-to-event navigation to the two surfaces that share the event-rendering pipeline (m.evs/m.vp): the live session view (stateSession) and the history transcript drill-in (s
…[truncated]
- 2026-07-02 context hints: 9 recorded with plan
- 2026-07-02 context hints: internal/tui/tui.go: updateSession ~L2899 (main key switch; '?' empty-input gating precedent ~L3015; m.picking branch as a model for a pre-switch mode); internal/tui/tui.go: updateHistory transcript b
…[truncated]
- 2026-07-02 implementer report: Implemented transcript search (`/`) and jump-to-event navigation for the live session view and the read-only history transcript (task 0116).  ## What changed (all in internal/tui/)  tui.go - Model sta
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The change adds transcript incremental search (`/`, n/N with wrap, esc-cancel) and jump-to-event navigation ({}()<>[]) to both the live session view and the read-only history transcript, exactly as sp
…[truncated]
- 2026-07-02 decision: accept — commit: tui: transcript search (/) with n/N cycling and jump-to-event keys (task 0116)
