---
id: "0153"
title: 'Web SPA interactions: send input, answer pickers, interrupt/stop'
status: todo
priority: 4
created: "2026-07-06"
updated: "2026-07-06"
depends_on:
    - "0152"
spec_refs:
    - docs/design/web-client.md#7. Phone-form-factor layout
---

## Description
Third slice of docs/design/web-client.md (§7 chrome, §9 item 3): make the web client interactive.

- Sticky bottom input bar → `SendInput` (prod running/idle sessions).
- `question_asked` raises a bottom-sheet answer picker: suggested options as buttons + free-text field; wired to `AnswerQuestion` (single; optionIndex >= 0 selects, -1 sends free text) and `AnswerQuestions` (batch, positional answers[i]); `question_answered` dismisses the sheet.
- Overflow menu (⋯) with `Interrupt` / `Resume` / `StopSession`.
- Auto-follow scroll pinned to newest while at bottom; scrolling up stops auto-follow and shows a "jump to latest" pill; new events must not yank the user's scroll position.
- Surface RPC errors (e.g. failed_precondition "no pending question") as non-fatal toasts.

## Acceptance criteria
- [ ] From a phone browser: send input to a live session, answer a single and a batched ask_user gate (option and free-text), interrupt/resume, and stop a session — all reflected in the event feed.
- [ ] Answer sheet dismisses on question_answered (including when answered from another client).
- [ ] Scroll behavior: auto-follow at bottom, pill when scrolled up, no scroll yank on new events.

## Work log
