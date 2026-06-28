---
id: "0048"
title: Default the backlog overlay view to hiding completed tasks
status: todo
priority: 3
created: "2026-06-27"
updated: "2026-06-27"
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
