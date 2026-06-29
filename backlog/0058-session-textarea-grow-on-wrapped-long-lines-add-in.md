---
id: "0058"
title: 'Session textarea: grow on wrapped long lines + add input behavior tests'
status: todo
priority: 4
created: "2026-06-29"
updated: "2026-06-29"
depends_on:
    - "0011"
spec_refs: []
---

## Description
Follow-ups from task 0011 review (commit 14f6b76).

## Background
The session input is now a `textarea` (internal/tui/tui.go). `syncInputHeight()` grows the box off `textarea.LineCount()` (logical, newline-delimited lines), so a single very long prompt that soft-wraps does NOT grow the box — it stays one visible row and scrolls internally. Also, no test directly exercises the new textarea behavior; the tests added alongside 0011 covered the (separate) previous-sessions flow.

## Acceptance criteria
- [ ] The input box grows for soft-wrapped long single lines too (use wrapped/visual line count, bounded by maxInputRows), not only explicit shift+enter newlines.
- [ ] Add unit tests for the session textarea: Enter sends+clears the buffer; shift+enter (and/or ctrl+j) inserts a newline without sending; height grows with content and is capped at maxInputRows; relayout keeps total rendered height within the terminal height.
- [ ] go build ./... and go test ./internal/tui/... pass.

## Acceptance criteria

## Work log
