---
id: "0071"
title: Render Edit tool output as a git-style colored diff
status: done
priority: 3
created: "2026-06-30"
updated: "2026-06-30"
depends_on: []
spec_refs: []
---

## Description
## Problem

Edit operations are currently rendered by simply showing the raw "old string" and "new string" side by side / sequentially. This is hard to read and doesn't communicate the change at a glance.

## Desired behavior

Render edits like a git diff:
- Unified diff style with removed lines highlighted in red and added lines highlighted in green.
- Show the formatted/syntactically meaningful underlying code (preserve indentation/whitespace), with appropriate line context around the change.
- Use the same styling conventions already in the TUI for consistency.

## Acceptance criteria

- [ ] Edit tool results display as a unified, git-style diff (computed by diffing old vs new content) rather than separate "old string" / "new string" blocks.
- [ ] Removed lines are shown in red, added lines in green, with leading `-`/`+` markers (or equivalent visual treatment).
- [ ] Surrounding/context lines are shown so the change is readable, not just the changed lines in isolation.
- [ ] Whitespace and indentation in the underlying code are preserved/displayed correctly.
- [ ] Rendering degrades gracefully for very large edits and respects terminal width.

## Acceptance criteria

## Work log
- 2026-06-30 plan: Render the Edit tool's call params as a unified git-style diff in the TUI.  Findings: - Edit tool (internal/tools/worker.go) takes file_path/old_string/new_string and returns a plain "edited <path>" r
…[truncated]
- 2026-06-30 implementer report: Implemented git-style unified diff rendering for Edit tool calls in the TUI.  Changes (internal/tui/tui.go): - Added `unifiedDiff(oldStr, newStr string, context int) string`: splits both sides into li
…[truncated]
- 2026-06-30 review tier: single-opus — reviewers: Claude
- 2026-06-30 review (Claude): accept — The change adds a proper line-level LCS unified diff (`unifiedDiff`/`diffOps`) and wires Edit tool calls in `cardParams` to render old_string vs new_string as a git-style diff via the existing `colori
…[truncated]
- 2026-06-30 decision: accept — commit: Render Edit tool calls as a git-style unified colored diff in the TUI  Add line-level LCS unifiedDiff/diffOps helpers and special-case Edit calls in cardParams to show old_string vs new_string as a un
…[truncated]
