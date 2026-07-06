---
id: "0141"
title: 'Clipboard yank (OSC 52): copy a transcript row / commit sha out of the TUI'
status: done
priority: 3
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 18. Client UI (TUI)
---

## Description
There is no way to get text *out* of the TUI: a commit sha, an error message, a model's answer, a file path. Terminal-native selection fights the alt-screen and mouse handling. OSC 52 clipboard writes work locally and over SSH (which matters for the remote-daemon story) and Bubble Tea supports them.

## Acceptance criteria
- [ ] `y` on a selected transcript row copies its body text (the expanded content, stripped of styling) to the clipboard via OSC 52.
- [ ] On a `commit_made` row, `y` copies the sha; on an error row, the error text.
- [ ] A brief flash confirms the copy ("copied ✓"); documented in the help modal per the maintenance contract in help.go.
- [ ] Gated like other bare-letter keys: never intercepts while the input has content.

## Plan

Goal: `y` in the live session view copies the selected transcript row's text to the clipboard via OSC 52 (Bubble Tea v2's tea.SetClipboard), with a transient "copied ✓" confirmation, gated like other bare-letter keys (only when the input is empty), and documented in help.go.

1. Yank text extraction — add a helper on *model, e.g. `yankText(ev *v1.Event) string`, that returns the text to copy for an event:
   - `commit_made` → dataField(ev, "sha")
   - `session_error` → the raw error text: sessionErrorHead(ev) headline (if any) + dataField(ev, "msg") — unwrapped/unstyled, not the rendered body
   - text-bearing events where the raw source is available and pastes better than the glamour-rendered body: `model_turn`/`user_input` → firstField(ev, "text", "report", "question", "answer"); `session_idle` → firstField(ev, "report"); `tool_result` → dataField(ev, "result"); `tool_call` → prettyArgs(dataField(ev, "args")) or the raw args
   - fallback for everything else → ansi.Strip(m.bodyFor(ev)), trimmed (this is the on-screen expanded content stripped of styling; strip trailing/leading whitespace)
   - return "" when there's nothing to copy.

2. Key handling — in updateSession's main key switch (near the other bare-letter keys like "q"/"/"), add case "y": only when `m.selected >= 0` AND `strings.TrimSpace(m.input.Value()) == ""` (never intercept mid-composition; fall through to the textarea otherwise). Resolve text := m.yankText(m.evs[m.selected]); if empty, return without a cmd (no-op). Otherwise return tea.Batch(tea.SetClipboard(text), <flash cmd>). Do NOT add it to the picker branch or search branch.

3. Confirmation flash — the existing flash machinery is error-only (m.flashErr, flashClearMsg{seq}, rendered "✗ …" in errStyle). Add a parallel transient notice: e.g. an m.flashNote string set by a small helper `noteFlash(msg string) tea.Cmd` that bumps flashSeq, clears flashErr, sets flashNote, and arms the same flashClearMsg{seq} tick (a shorter ~2s duration is fine). Clear flashNote wherever flashErr is cleared (clearFlash, the flashClearMsg handler around line 645/2447). Render it in statusBar() as a high-priority segment in successStyle ("copied ✓") mirroring the flashErr segment. Keep flash(err) behavior unchanged.

4. Help modal — per the maintenance contract in help.go, add to the "session" section: {"y", "copy the selected row to the clipboard (commit → sha, error → message; input empty)"}.

5. Tests (internal/tui/tui_test.go, following existing patterns with keyMsg/drive):
   - yankText unit coverage: commit_made returns the sha; session_error returns the error text; a model_turn returns its text.
   - pressing "y" on a selected commit_made row with empty input sets flashNote ("copied ✓") and returns a cmd; with text in the input, "y" types into the textarea (no flash, input value gains "y").
   - flashNote clears on flashClearMsg with matching seq.

6. Verify: go build ./... && go test ./internal/tui/...

Scope note: the read-only session-browser transcript replay is NOT required by the acceptance criteria; add `y` there only if it trivially reuses the same helper, otherwise leave it out.

### Starting points
- internal/tui/tui.go:3622-3785 — updateSession main key switch; gating pattern: strings.TrimSpace(m.input.Value()) == ""
- internal/tui/tui.go:9208 renderBody / 9039 bodyFor / 6751 searchableText (ansi.Strip pattern)
- internal/tui/tui.go:778 flash / 794 clearFlash / flashClearMsg handler ~line 645 & 2447; statusBar ~8334 renders flashErr segment
- internal/tui/help.go — session helpSection + maintenance contract
- tea.SetClipboard is in charm.land/bubbletea/v2 (clipboard.go) — emits OSC 52
- internal/tui/tui_test.go:1253 drive(), 1262 keyMsg() test helpers
- dataField/firstField helpers ~tui.go:10441/10552; sessionErrorHead ~7122

## Work log
- 2026-07-06 plan: Goal: `y` in the live session view copies the selected transcript row's text to the clipboard via OSC 52 (Bubble Tea v2's tea.SetClipboard), with a transient "copied ✓" confirmation, gated like othe
…[truncated]
- 2026-07-06 context hints: 7 recorded with plan
- 2026-07-06 context hints: internal/tui/tui.go:3622-3785 — updateSession main key switch; gating pattern: strings.TrimSpace(m.input.Value()) == ""; internal/tui/tui.go:9208 renderBody / 9039 bodyFor / 6751 searchableText (ans
…[truncated]
- 2026-07-06 implementer report: Implemented Task 0141 — OSC 52 clipboard yank (`y`) in the live session view.  Changes (all in internal/tui): - tui.go:   - Added `flashNote` field alongside `flashErr`/`flashSeq` (shares the flashS
…[truncated]
- 2026-07-06 review tier: single-opus — reviewers: Claude
- 2026-07-06 review (Claude): accept — The change implements OSC 52 clipboard yank on `y` in the live session view exactly per the plan and acceptance criteria: a yankText helper extracts the right text per event type (commit_made→sha, s
…[truncated]
- 2026-07-06 decision: accept — commit: tui: clipboard yank via OSC 52 — `y` copies the selected transcript row (commit → sha, error → message) with a "copied ✓" flash (task 0141)
- 2026-07-06 usage: 22,527 tok (in 146, out 22,381, cache_r 2,020,386, cache_w 100,322) · cost n/a (unpriced)
  implementer: 10,986 tok (in 72, out 10,914, cache_r 1,062,233, cache_w 40,757) · cost n/a (unpriced)
  coordinator: 9,556 tok (in 56, out 9,500, cache_r 892,507, cache_w 47,340) · cost n/a (unpriced)
  reviewer:Claude: 1,985 tok (in 18, out 1,967, cache_r 65,646, cache_w 12,225) · cost n/a (unpriced)
