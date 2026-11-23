---
id: "0596"
title: Work-loop sessions claim tasks atomically and choose with sibling awareness
status: todo
priority: 2
created: "2026-11-23"
updated: "2026-11-23"
depends_on: []
spec_refs:
    - spec.md#9.1 Unattended work loop
---

## Description
Status: todo. First step of the parallel work-loop plan (user decision 2026-11-23, session s_a96d1f53776eaa46). It is useful at N=1 because loops already run beside hand-started sessions. 0598 (parallel loop slots) builds on it.

## Why
Work sessions choose their own task, which the user wants to keep: agents reading the backlog find a better order than priority plus id. Two flaws in how choices are recorded showed up in wheeler, the user's data-pipeline project. On 2026-11-23, seven sessions told to "choose the next ready task" ran in parallel:
- Holds were invisible. `task_focus` is emitted only by `update_task`, `spawn_implementer`, `spawn_reviewers`, `re_review` and `switch_to_work` (`orchestrator.go` `emitFocus` callers). Three of the seven sessions (s_fce11aa70fac504a, s_73ed52de33aaafc8, s_a865539bec3b689c) edited backlog files directly and never held their task.
- Claims are not exclusive. 8–9 s passed between `list_backlog` and `update_task` (s_2c430d6bcafac6d2, s_4a7ca49b30cbbb04), and nothing stops two sessions from claiming one task.
- Priority inversion. p2 tasks 0228 and 0348 were opened by 2–4 sessions each. Each session took a p3 because the p2s "all look heavy" (s_4a7ca49b30cbbb04 event 18).

User decisions (2026-11-23, s_a96d1f53776eaa46):
- Agents choose; the loop does not assign.
- Size is not a reason to skip a task. Splitting is fine when necessary, but not endlessly: an earlier session turned one task into about 100.

## Acceptance criteria
- Claiming a task fails when another live, actively working session in the workspace holds it, with the same hold definition as `heldTasks`. The error names the holder. This covers `update_task` to in_progress and every other path that emits focus.
- A work session's file-tool edit of a task's backlog file records focus for that task. If another session holds the task, the tool result names the holder.
- The loop session prompt says:
  - other loop and live sessions may be working in parallel (`list_backlog` footer, intents);
  - choose the best next task considering backlog order and what siblings are doing;
  - size is not a reason to skip;
  - split a task at most once per session, then work the first piece in the same session;
  - state, in `set_intent`, the areas or files and the external resources (hosts, services, GPUs, databases) the work will touch.
- Loop progress for a session is its own commits, or changes to the backlog files of tasks it focused. Creating tasks alone is not progress. The digest reports tasks created per session.
- Tests cover two sessions racing to claim one task (exactly one wins) and focus recorded from a backlog-file edit.
- spec.md §9.1 records the behavior.

## Work log
