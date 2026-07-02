---
id: "0125"
title: 'TUI: input box dead after answering the final picker question (onboarding)'
status: done
priority: 2
created: "2026-07-02"
updated: "2026-07-02"
depends_on: []
spec_refs: []
---

## Description
## Bug

After the onboarding agent finished, the session input box dropped all keystrokes.

Root cause: a `question_asked` with options blurs the textarea (`m.input.Blur()`). When the answer is committed **via the picker** and no further free-text question follows — a single options question, or a multi-question wizard whose *last* question is a picker — nothing ever called `m.input.Focus()` again. `m.picking` was cleared, so keys fell through to `m.input.Update(msg)`, but the bubbles textarea silently ignores keys while blurred → dead input for the rest of the session. (The mixed picker→free-text case was already fixed; the picker-last case was missed.)

## Fix

Re-focus the textarea at all three collapse points in `internal/tui/tui.go`:
- `choosePickerOption` (single-question path) — batches the blink cmd with the answer RPC.
- `recordWizAnswer` final-question submit path — same.
- `appendEvent`'s `question_answered` handler — safety net (skipped while transcript search owns input; the discarded cmd is only cursor blink).

## Acceptance criteria

- [x] Answering a single options question via the picker leaves the textarea focused.
- [x] A wizard batch ending in a picker question leaves the textarea focused after submit.
- [x] A `question_answered` event alone re-focuses (safety net).
- [x] Regression tests: `TestPickerAnswerRefocusesInput`, `TestWizardFinalPickerAnswerRefocusesInput`, `TestQuestionAnsweredEventRefocusesInput`.

## Acceptance criteria

## Work log
