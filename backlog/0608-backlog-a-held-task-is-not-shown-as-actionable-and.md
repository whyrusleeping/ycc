---
id: "0608"
title: 'Backlog: a held task is not shown as actionable, and the Held chip names the holder (web + iOS)'
status: done
priority: 3
created: "2026-11-24"
updated: "2026-11-25"
depends_on: []
spec_refs:
    - spec.md#9.1
---

## Description
Current state: todo. Accepted by the user 2026-11-24 in session s_5a6bb9c25a12632a (UX review of the live wheeler work loop).

Spec §9.1: the work loop skips ready tasks held by another live session, and work sessions' backlog views mark held tasks not actionable. The client backlog views do not follow that.

Observed 2026-11-24 on wheeler's backlog during loop `loop_d074bc58`: in the web table, in-progress tasks 0470, 0449, 0348, 0342, 0317 and 0272 each carried a "held" chip and, in the Ready column, "✓ actionable", whose tooltip says "Accepted work whose dependencies are done: the work loop may pick it" (`clients/web/src/features/backlog/model.ts`). The iOS board shows a "Held" chip on the same cards. Neither chip says which session holds the task.

## Acceptance criteria
- Web table and board, and iOS list and board, show a held task's readiness as held rather than actionable/Ready, and the web "Actionable only" filter excludes held tasks.
- The held chip names the holding session (title or short id, and that it is a loop session when it is) and opens it.
- If the backlog response lacks the holder's session id, the proto change and its test are part of this task.
- Web typecheck and vitest pass; the iOS change is built and screenshot-checked on the Mac simulator.

## Outcome

ListBacklog rows and GetTask/UpdateTask details carry `held_by` (TaskHolder: session id, title, loop flag) from the work loop's heldTasks rule (exact workspace, independent of session coordination) via Manager.TaskHolders. Web and iOS backlog table/list, board and task detail read it: a held task shows "held" instead of actionable/Ready, is left out of "Actionable only" and the actionable sort, and its chip names the holder ("loop s_…" for loop sessions, else the title) and opens it; the open web pane follows the polled backlog row. The notice-board-derived holder lookups were removed. Tests: TestTaskHoldersFollowHeldTasksRule, e2e TestE2EBacklogHeldBy (claim, hold while waiting, release), web backlog/coordination/heldTaskDetail tests; Mac xcodebuild and YccKit tests pass, simulator and Playwright screenshots checked. Bindings and internal/web/dist regenerated from HEAD + 0608 only.

Commit: 0608: backlog held_by — held tasks are not actionable and the Held chip names its holder (web + iOS)
