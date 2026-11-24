---
id: "0608"
title: 'Backlog: a held task is not shown as actionable, and the Held chip names the holder (web + iOS)'
status: todo
priority: 3
created: "2026-11-24"
updated: "2026-11-24"
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

## Work log
