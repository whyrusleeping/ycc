---
id: "0048"
title: Default the backlog overlay view to hiding completed tasks
status: done
priority: 3
created: "2026-06-27"
updated: "2026-06-28"
depends_on: []
spec_refs: []
---

## Description
The backlog overlay view currently shows completed (done) tasks alongside open ones. It should default to hiding already-completed tasks so the list stays focused on actionable work.

## Acceptance criteria
- When the backlog overlay is opened, completed (done) tasks are hidden by default.
- A way to toggle showing completed tasks (e.g. a keybinding/filter) remains available, so they can still be viewed on demand.
- Non-done tasks (todo/in_progress/blocked) continue to display as before.
- The default-hidden behavior is consistent with the backlog listing convention (done tasks hidden unless explicitly included).

## Acceptance criteria

## Work log
- 2026-06-28 plan: Filter completed tasks out of the backlog overlay list view client-side, defaulting to hidden, with a toggle key.  Changes in internal/tui/tui.go: 1. Add a `backlogShowDone bool` field to the model (n
…[truncated]
- 2026-06-28 implementer report: Implemented default-hidden completed tasks in the backlog overlay (internal/tui/tui.go):  - Added `backlogShowDone bool` model field (default false). - Added `visibleBacklogTasks()` helper that filter
…[truncated]
- 2026-06-28 review tier: simple (coordinator self-review)
- 2026-06-28 decision: accept — commit 7c62644: TUI backlog overlay: hide done tasks by default, toggle with 'd' (task 0048)
- 2026-06-28 usage: 12,058 tok (in 64, out 11,994, cache_r 543,691, cache_w 32,654) · cost n/a (unpriced)
