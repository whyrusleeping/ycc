---
id: "0413"
title: 'Desktop web: backlog browser and task editing, quick capture'
status: in_progress
priority: 3
created: "2026-10-02"
updated: "2026-10-05"
depends_on:
    - "0410"
spec_refs:
    - Backlog browser
    - Backlog
    - docs/design/web-client.md#Data access
---

## Description
Fourth phase of docs/design/web-client.md. Reference iOS BacklogView/BacklogModel, TaskDetailView/TaskDetailModel, QuickCaptureView, and spec §6.2/§18.5.

- Backlog view: ListBacklog as a sortable, filterable table (status, priority, dependencies, actionable flag, done toggle) with keyboard navigation.
- Task detail in the inspector or full pane: GetTask, rendered markdown body and work log, edits to user-maintained frontmatter and body via UpdateTask. A failed save keeps the draft and shows the error. A successful save replaces state with the canonical response.
- Status changes, including promoting proposed → todo. New task via CreateTask. Quick capture (palette + shortcut) via CaptureBacklogItem.
- Links between tasks (dependencies) and from sessions that reference focused tasks.

## Acceptance criteria
- [ ] Browse, filter, edit, create, capture, and promote tasks from the browser. Changes appear in `ycc` CLI/TUI views.
- [ ] A concurrent-edit or validation failure doesn't lose the user's draft.

## Work log
