---
id: "0058"
title: 'Session textarea: grow on wrapped long lines + add input behavior tests'
status: done
priority: 4
created: "2026-06-29"
updated: "2026-06-30"
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
- 2026-06-30 plan: Make the session textarea grow on soft-wrapped long lines (not just explicit newlines) by switching to the bubbles/v2 textarea's built-in DynamicHeight, then add focused unit tests.  Implementation (i
…[truncated]
- 2026-06-30 implementer report: Implemented task 0058.  Changes in internal/tui/tui.go: - newSessionInput(): enabled the bubbles/v2 textarea's built-in DynamicHeight (set DynamicHeight=true, MinHeight=1; kept MaxHeight=maxInputRows 
…[truncated]
- 2026-06-30 review tier: single-opus — reviewers: Claude
- 2026-06-30 review (Claude): accept — Task 0058 is correctly and completely implemented. The session textarea now grows on soft-wrapped long lines via the bubbles/v2 textarea's built-in DynamicHeight (MinHeight=1, MaxHeight=maxInputRows),
…[truncated]
- 2026-06-30 decision: accept — commit: Session textarea grows on soft-wrapped long lines + input behavior tests (task 0058)  Enable the bubbles/v2 textarea DynamicHeight so the session input grows from total visual (soft-wrapped) lines, bo
…[truncated]
- 2026-06-30 usage: 13,048 tok (in 66, out 12,982, cache_r 660,355, cache_w 50,998) · cost n/a (unpriced)
