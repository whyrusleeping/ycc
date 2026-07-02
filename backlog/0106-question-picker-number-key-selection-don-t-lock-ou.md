---
id: "0106"
title: 'Question picker: number-key selection + don''t lock out scrolling/browsers'
status: done
priority: 3
created: "2026-07-01"
updated: "2026-07-02"
depends_on: []
spec_refs:
    - 18.3 Structured interactive questions (Claude-Code style)
---

## Description
## Description
While an ask_user options picker is active (`m.picking`), every key except ↑/↓/enter/ctrl+c is swallowed. The user cannot scroll the transcript to re-read the question's context, cannot open the backlog browser (often exactly what's needed to answer "which task next?"), and cannot press `1`–`9` to pick — though spec §18.3 promises "arrow-key/number navigable".

## Acceptance criteria
- [ ] Digits 1–9 select the corresponding option directly (single-question picker and wizard steps)
- [ ] pgup/pgdn and mouse wheel scroll the transcript while a picker is active
- [ ] ctrl+b (backlog) and ctrl+n (capture) work while a question is pending; the picker re-renders on return
- [ ] Same affordances in the multi-question wizard
- [ ] Footer hint documents number selection

## Acceptance criteria

## Plan

Goal: while an ask_user options picker is active (`m.picking`), stop swallowing useful keys — add number-key selection, keep the transcript scrollable, and let the backlog browser / quick-capture overlays open.

All changes live in `internal/tui/tui.go` (`updateSession`, key handling under `if m.picking { ... }` around line 2330) plus tests in `internal/tui/tui_test.go`.

1. Extract a helper on *model, e.g. `choosePickerOption(idx int) tea.Cmd`, containing the existing enter-selection logic (wizard branch via `recordWizAnswer(idx, opt, true)`, otherwise clear pending/pickerOpts, set follow, relayout, `answerQuestion(idx, "")`). Use it from the existing "enter" case (when cursor < len(opts)).

2. Digits 1–9: in the picking key switch, handle keys "1".."9"; if the digit ≤ len(m.pickerOpts), select that option directly via the helper (idx = digit-1). Digits beyond the option count are ignored. This automatically covers both the single-question picker and wizard picker steps since they share this code path. (Wizard free-text steps keep the textarea — digits must still type there; that's already the non-picking path.)

3. Scrolling while picking: add "pgup"/"pgdown" cases inside the picking block mirroring the non-picking ones (`m.vp.HalfPageUp()/HalfPageDown()`; update `m.follow = m.vp.AtBottom()`). Mouse wheel already works (the MouseWheelMsg case precedes the KeyMsg case and doesn't check m.picking) — verify with a test rather than change code.

4. Overlays while picking: add "ctrl+n" (call `m.openCapture()`) and "ctrl+b" (set backlog browser state + `m.fetchBacklog`, same as the non-picking case) inside the picking block. When those overlays close, `m.picking` is untouched so sessionView re-renders the picker — verify in a test.

5. Footer hints: update the two picking help lines in `sessionView` (wizard + single) to advertise number selection and the restored keys, e.g. " ↑↓/1–9 choose · enter select · pgup/pgdn scroll · ctrl+b backlog · esc settings" (keep the wizard's ‹other…› hint). footer() already clamps width so long lines are safe.

6. Tests (tui_test.go, follow existing test style with model literals + Update calls):
   - digit key selects option N and sends the answer (single question) and advances the wizard (multi-question).
   - digit > len(options) is a no-op.
   - pgup/pgdown while picking scrolls the viewport instead of being swallowed.
   - ctrl+b while picking opens the backlog browser and picker state survives (m.picking still true after closing).
   - ctrl+n while picking opens the capture overlay.
   - mouse wheel while picking still reaches the viewport.
   - footer hint while picking mentions number selection.

Verify: `go build ./... && go test ./internal/tui/ ./...`.

### Starting points
- internal/tui/tui.go:2305 updateSession — picking key block at ~2330
- internal/tui/tui.go:1185 recordWizAnswer, 1209 answerQuestion
- internal/tui/tui.go:5386 sessionView — picking footer hints at 5395 and 5402
- internal/tui/tui.go:2368-2377 non-picking ctrl+n/ctrl+b handlers to mirror
- internal/tui/tui_test.go — existing Update-driven tests to copy style from

## Work log
- 2026-07-02 plan: Goal: while an ask_user options picker is active (`m.picking`), stop swallowing useful keys — add number-key selection, keep the transcript scrollable, and let the backlog browser / quick-capture ov
…[truncated]
- 2026-07-02 context hints: 5 recorded with plan
- 2026-07-02 context hints: internal/tui/tui.go:2305 updateSession — picking key block at ~2330; internal/tui/tui.go:1185 recordWizAnswer, 1209 answerQuestion; internal/tui/tui.go:5386 sessionView — picking footer hints at 5
…[truncated]
- 2026-07-02 implementer report: Implemented task 0106: number-key picker selection + don't lock out scrolling/browsers while an ask_user picker is active.  Changes in `internal/tui/tui.go`: - Added `func (m *model) choosePickerOptio
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The change fully satisfies task 0106. A shared choosePickerOption helper is extracted and used by both the enter and new digit-key paths; digits 1–9 select options (with out-of-range digits ignored)
…[truncated]
- 2026-07-02 decision: accept — commit: tui: number-key picker selection; keep scrolling and backlog/capture live while a question is pending (0106)
- 2026-07-02 usage: 20,401 tok (in 114, out 20,287, cache_r 1,093,389, cache_w 74,285) · cost n/a (unpriced)
  implementer: 12,361 tok (in 64, out 12,297, cache_r 643,377, cache_w 31,237) · cost n/a (unpriced)
  coordinator: 5,943 tok (in 32, out 5,911, cache_r 390,332, cache_w 29,379) · cost n/a (unpriced)
  reviewer:Claude: 2,097 tok (in 18, out 2,079, cache_r 59,680, cache_w 13,669) · cost n/a (unpriced)
