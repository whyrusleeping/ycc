---
id: "0090"
title: Make backlog task detail view scrollable in the TUI viewer
status: done
priority: 3
created: "2026-06-30"
updated: "2026-06-30"
depends_on: []
spec_refs: []
---

## Description
## Problem
When viewing an individual task from the backlog viewer, the content cannot be scrolled. Tasks with long descriptions, acceptance criteria, or work logs get cut off at the bottom and there is no way to reach the rest of the content.

## Expected
The task detail view should be a scrollable view, allowing the user to scroll down (and back up) through the full task content regardless of length.

## Acceptance Criteria
- [ ] The backlog task detail view supports vertical scrolling (e.g. arrow keys / PgUp/PgDn and/or mouse wheel as appropriate for the TUI).
- [ ] Long task content (description + acceptance criteria + work log) is fully reachable by scrolling.
- [ ] Scroll position resets/behaves sensibly when opening a different task.
- [ ] Existing keybindings (e.g. back/close) continue to work.

## Acceptance criteria

## Work log
- 2026-06-30 plan: Make the backlog task detail view scrollable by giving it its own viewport, mirroring the existing read-only transcript drill-in (updateHistory/transcriptView) pattern.  Files: internal/tui/tui.go (+ 
…[truncated]
- 2026-06-30 implementer report: Made the backlog task detail view scrollable in internal/tui/tui.go by giving it its own viewport, mirroring the transcript drill-in pattern.  Changes: - Added `backlogVP viewport.Model` field to the 
…[truncated]
- 2026-06-30 review tier: single-opus — reviewers: Claude
- 2026-06-30 review (Claude): accept — The change makes the backlog task detail view a full-screen scrollable viewport, faithfully mirroring the existing read-only transcript drill-in pattern (titleBar + viewport + footerBar). It adds a ba
…[truncated]
- 2026-06-30 decision: accept — commit: tui: make backlog task detail view scrollable (task 0090)
