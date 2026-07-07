---
id: "0174"
title: 'TUI cost view: per-task drill-down with agent breakdown'
status: todo
priority: 4
created: "2026-07-07"
updated: "2026-07-07"
depends_on:
    - "0173"
spec_refs:
    - "20.5"
---

## Description
The TUI cost modal (spec §20.5, task 0039) currently supports a single group-by dimension cycled with "g" (task|model|session|day|agent). It cannot show the per-task per-agent breakdown ("task 0093: coordinator $12, implementer $19, reviewer $5") that the CLI provides via `ycc cost --by task,agent`, and there is no per-task drill-down.

Add a drill-down: with the cost view grouped by task, pressing enter on a task row opens that task's breakdown grouped by agent (using the GetUsage task filter from task 0173). Esc goes back up to the task table rather than closing the modal outright.

Acceptance criteria:
- In the cost view grouped by task, enter on a row shows only that task's usage, grouped by agent, with a TOTAL row; title/header makes the focused task obvious.
- Within the drill-down, "g" still cycles dimensions (e.g. agent → model) while staying filtered to the task.
- Esc from the drill-down returns to the task-level table (cursor preserved); esc again closes the modal.
- Works for the "(unattributed)" row (empty task id) too, or that row is clearly non-drillable.
- Snapshot/TUI tests cover the drill-down rendering.

## Acceptance criteria

## Work log
