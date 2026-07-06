---
id: "0149"
title: Wrap long model questions in the TUI instead of clipping at screen edge
status: done
priority: 3
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs: []
---

## Description
## Problem

In PM mode (possibly other modes too — unverified), when the model asks the user a question whose text is longer than the terminal width, the question is clipped at the right edge of the screen instead of wrapping. The overflowing text is unreadable.

## Notes

- Reproduced in PM mode; check whether the same rendering path is used for questions in other modes/session views and fix generally rather than per-mode.
- Likely a missing word-wrap (e.g. lipgloss width/wrap) on the question rendering path in `internal/tui`.

## Acceptance Criteria

- [ ] A model question longer than the terminal width wraps onto multiple lines and is fully readable.
- [ ] Wrapping re-flows correctly on terminal resize.
- [ ] Verified in PM mode and any other mode sharing the question rendering path.
- [ ] Regression test (or snapshot test) covering long question wrapping.

## Plan

## Root cause

The transcript body path already word-wraps questions (glamour markdown / wrapTo). The clipping happens in the FOOTER question paths, which clamp to one physical row:

1. `pickerView` (internal/tui/tui.go ~8795): when a `question_asked` event carries options, the footer picker shows the prompt via `questionPrompt(m.pending, m.w-6, false)` → `oneLine` → `trunc`, i.e. hard-truncated with an ellipsis. Meanwhile the transcript's `questionBody` collapses that event to "answer below ↓" while `m.picking` is set — so a long question is *nowhere* fully readable. This is the reported bug (PM/"n" mode uses the same session view as every mode, so the fix is general).
2. `wizardView` (~8822): the multi-question wizard truncates each prompt with `trunc(prompt, m.w-len(num)-4)` — same defect for batch questions.

The layout is safe for multi-line footers: `footerStackHeight()` (~2111) measures the *rendered* `pickerView()`/`wizardView()` strings with `lipgloss.Height`, and `relayout()` runs on picker/wizard state changes and on `WindowSizeMsg`, so wrapping reflows automatically on resize.

## Changes (all in internal/tui/tui.go + tests)

1. **pickerView**: render the pending question word-wrapped instead of one-line-clamped. Wrap to the available width (terminal width minus the badge column) using the existing `wrapTo` (word-wrap then hard-wrap so unbroken tokens can't overflow), with continuation lines indented to align under the first line's text (hanging indent equal to the badge width: `" " + askStyle(" ? ") + " "` = 5 cols).
   - Cleanest shape: make `questionPrompt`'s wrapped path produce the hanging indent (adjusting the one other caller, the capture overlay at ~3993, whose width arg must account for the indent), OR give pickerView its own small wrapped rendering and drop the now-unused `wrapField=false` path. Implementer's choice; update `questionPrompt`'s doc comment either way (its "session picker footer … exactly one row tall" note is stale — there is no such caller anymore).
2. **wizardView**: wrap each question prompt the same way (hanging indent aligned after the `▸ N. ` prefix) instead of `trunc`. Keep the per-question answer echo lines (`→ …`) single-line as today — answers fold fully into the transcript once submitted; only the *question* readability is in scope.
3. Leave picker **option rows** single-line-clamped (existing deliberate behavior with its own comment).

## Tests (internal/tui/tui_test.go)

- Regression test: a `question_asked` event with options and a prompt much longer than the terminal width → `pickerView()` output contains the full prompt text (whitespace-normalized across wrapped lines) and every rendered line fits within `m.w` (`lipgloss.Width <= m.w`).
- Resize reflow: after a `WindowSizeMsg` with a narrower width, the picker re-wraps and still fits; `footerStackHeight()` matches `lipgloss.Height(pickerView())` so the help line/viewport accounting stays correct.
- Same coverage for the wizard: long prompt in a multi-question batch wraps in `wizardView()`.
- Existing helpers `newPickerModel` (~3285) and the picker tests around ~4886 show how to drive the model with synthetic events.

## Verification

`go build ./... && go test ./internal/tui/`, plus `go vet ./internal/tui/`.

### Starting points
- internal/tui/tui.go: pickerView ~8795, questionPrompt ~8782, wizardView ~8822
- internal/tui/tui.go: footerStackHeight ~2111, relayout ~2093 (measures rendered footer strings — multi-line is safe)
- internal/tui/tui.go: wrapTo ~9254 (wordwrap+wrap pairing), oneLine/trunc ~10647
- internal/tui/tui.go: questionBody ~9170 collapses to 'answer below ↓' while m.picking — why the footer must carry the full text
- internal/tui/tui_test.go: newPickerModel ~3285, picker option tests ~4886

## Work log
- 2026-07-06 plan: ## Root cause  The transcript body path already word-wraps questions (glamour markdown / wrapTo). The clipping happens in the FOOTER question paths, which clamp to one physical row:  1. `pickerView` (
…[truncated]
- 2026-07-06 context hints: 5 recorded with plan
- 2026-07-06 context hints: internal/tui/tui.go: pickerView ~8795, questionPrompt ~8782, wizardView ~8822; internal/tui/tui.go: footerStackHeight ~2111, relayout ~2093 (measures rendered footer strings — multi-line is safe); i
…[truncated]
- 2026-07-06 implementer report: Fixed long model questions being clipped at the screen edge in the TUI footer question paths.  ## Changes (internal/tui/tui.go) 1. **questionPrompt**: replaced the dual-mode signature `(prompt, w, wra
…[truncated]
- 2026-07-06 review tier: single-opus — reviewers: Claude
- 2026-07-06 review (Claude): accept — The change correctly fixes the footer question-clipping bug by word-wrapping the pending question in both pickerView and wizardView with proper hanging indents, using the existing wrapTo (wordwrap+har
…[truncated]
- 2026-07-06 decision: accept — commit: tui: word-wrap long model questions in the picker/wizard footer instead of clipping (task 0149)
