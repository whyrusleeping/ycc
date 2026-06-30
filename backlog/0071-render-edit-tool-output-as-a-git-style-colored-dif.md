---
id: "0071"
title: Render Edit tool output as a git-style colored diff
status: todo
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
