---
id: "0027"
title: Record session→task focus for cost attribution
status: todo
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
