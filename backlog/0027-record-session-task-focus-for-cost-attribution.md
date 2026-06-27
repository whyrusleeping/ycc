---
id: "0027"
title: Record session→task focus for cost attribution
status: done
priority: 2
created: "2026-06-26"
updated: "2026-06-26"
depends_on:
    - "0004"
spec_refs:
    - Token usage & cost accounting
    - Modes (the home menu)
    - The work orchestration (in detail)
---

## Description
Durably link a session to the backlog task(s) it works on so usage can be grouped "by
backlog task" (spec §20.2). Today the task a `work` session operates on lives only in the
kickoff prompt — nothing in the durable event log records it.

## Context
- The `pm → work` hand-off (`switch_to_work`) already knows the explicit target task id.
- The `work` coordinator picks/accepts a task and already references its id via
  `update_task`→`in_progress`, `propose_plan`, and `spawn_implementer(task_id=…)`.
- A session may touch more than one task; attribution should use the active focus at the
  time of each `model_turn`.

## Acceptance criteria
- [ ] New `task_focus` event type (`data: { task: "0007" }`) in `internal/event`.
- [ ] Emitted when focus is established: carried by `switch_to_work` and/or emitted by the
      work coordinator when it picks/accepts a task (e.g. on `update_task`→in_progress /
      `spawn_implementer`). Avoid spurious duplicate focus events for the same task.
- [ ] The event projection (`event.Reduce`) tracks the current focused task and can
      attribute subsequent turns to it (turns before any focus → "unattributed").
- [ ] Tests cover focus emission on the hand-off and on coordinator task pickup, and that
      the projection attributes turns to the right task.

## Acceptance criteria

## Work log
- 2026-06-26 plan: Implement durable session→task focus attribution (spec §20.2).  1. Event type: In `internal/event`, add a new `task_focus` event type with payload `{ task: "0007" }`. Mirror the existing event-type
…[truncated]
- 2026-06-26 implementer report: Implemented durable session→task focus attribution for cost grouping (spec §20.2).  Changes: - internal/event/event.go: added new `TaskFocus` event type (`task_focus`, `data: { task: "0007" }`), do
…[truncated]
- 2026-06-26 review (claude): accept — The change fully satisfies the task. It adds a new `task_focus` event type (`data: { task }`) in internal/event, emits focus on the pm→work hand-off (switch_to_work), on coordinator task pickup (upd
…[truncated]
- 2026-06-26 revision: Addressed the reviewer nit on Render().  Changes: - internal/event/event.go: added "task" to the Render() default-case key list so task_focus events surface their task id in the terse human-facing lin
…[truncated]
- 2026-06-26 review (claude): accept — The revision addresses the prior nit: Render now includes "task" in its key list so task_focus events display the focused task id in the terse stream, with a dedicated test (TestRenderTaskFocus). The 
…[truncated]
- 2026-06-26 decision: accept — commit 6426920: Record session→task focus for cost attribution  Add task_focus event type linking a session to the backlog task it works on. Emit focus on the pm→work hand-off, update_task→in_progress, and spaw
…[truncated]
