---
id: "0030"
title: Let the work coordinator create backlog tasks (split + follow-on work)
status: todo
priority: 2
created: "2026-06-26"
updated: "2026-06-26"
depends_on: []
spec_refs: []
---

## Description
## Description

The `work` coordinator should be able to add new backlog items on its own during an implementation session, for two cases:

1. **Splitting** — when a single task turns out to be too big, it can break out the
   remaining/secondary scope into new, well-scoped tasks (optionally depending on the
   current one) instead of cramming everything into one commit.
2. **Follow-on work** — when, after implementing, it notices worthwhile follow-up
   (refactors, hardening, tests, discovered bugs), it can capture that as new backlog
   tasks rather than silently dropping it or scope-creeping the current task.

This is already implied by spec §8, which lists `create_task` among the coordinator
tools — but the actual work coordinator (`CoordinatorTools` in
`internal/orchestrator/orchestrator.go`) does **not** wire `create_task` in. So this is
both a capability gap and a spec/code drift.

### Implementation sketch
- Add `createTask(d)` to the `CoordinatorTools` registry (it already exists, used by `pm`).
- Extend `coordinatorSystem` (`internal/orchestrator/prompts.go`) with brief guidance:
  the session still drives ONE task to completion, but it may use `create_task` to (a)
  split out scope that doesn't belong in this commit and (b) record follow-on work it
  discovers — keeping the current task focused rather than expanding its scope. New tasks
  should have clear titles/acceptance criteria and appropriate `depends_on`.
- Update spec §10 (work orchestration) with a short note that the coordinator may spin
  off split/follow-on tasks via `create_task`; keep §8 consistent.

## Acceptance criteria
- `create_task` is available to the `work` coordinator (in `CoordinatorTools`).
- Coordinator prompt guides when to split vs. record follow-on work, without encouraging
  scope creep on the active task.
- spec §10 mentions the split/follow-on capability; §8 stays accurate.
- Existing tests pass; a test asserts the work coordinator registry includes `create_task`.

## Work log


## Acceptance criteria

## Work log
- 2026-06-26 plan: 1. In `internal/orchestrator/orchestrator.go`, add `createTask(d)` to the `CoordinatorTools` registry (the function already exists and is used by pm). Place it near `updateTask(d)`. 2. In `internal/or
…[truncated]
