---
id: "0168"
title: 'Work loop: consider tasks added during the loop when picking future work'
status: todo
priority: 3
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs: []
---

## Description

Tasks that get added while a work-loop session is running (e.g. split-off / follow-on tasks the coordinator files via `create_task`, or user quick-capture adds via ctrl+n / `ycc task add`) do not appear to be considered by the same work loop when it picks the next task. First verify whether this is actually the case: check how the loop driver (TUI "work (loop)" toggle) selects the next task between iterations — whether it re-reads the live backlog each cycle or works from a snapshot/queue built when the loop started.

If confirmed, fix it so newly created tasks are pushed onto the loop's queue for future iterations, with the same eligibility rules as the initial pick:

- status is `todo` (never `proposed`, `blocked`, or `in_review`)
- all dependencies are done (`[READY]`)
- it doesn't require user input (respect the blocked/needs-user semantics)

Note: within a single coordinator session the "THE BACKLOG IS LIVE" prompt guidance already tells the coordinator not to chase mid-session additions — this task is about the loop driver's between-session selection, not changing that in-session behavior.

## Acceptance criteria

- Investigation note recorded (work log or code comment) confirming or refuting the current behavior.
- A task created mid-loop with status `todo` and satisfied dependencies is picked up by a subsequent loop iteration without restarting the loop.
- Tasks that are `proposed`, `blocked`, dependency-blocked, or otherwise need user input are NOT picked up.
- Test covering the mid-loop task-addition path.

## Work log
