---
id: "0604"
title: 'Work-loop digest headline: count all tasks done and live sessions; reword "landed manually"'
status: todo
priority: 2
created: "2026-11-24"
updated: "2026-11-24"
depends_on: []
spec_refs:
    - spec.md#9.1
---

## Description
Current state: todo. Accepted by the user 2026-11-24 in session s_5a6bb9c25a12632a (UX review of the live wheeler work loop).

The loop summary line is built in the clients (`summaryLine` in `clients/web/src/features/workloop/model.ts`, and the matching counts in `clients/ios/YccKit/Sources/YccKit/WorkLoopModel.swift`) from `sessions`, `completed`, `created` and `manual_landings`. Observed 2026-11-24 on wheeler loop `loop_d074bc58` after 3 hours:

- The headline read "11 sessions · 4 completed, 19 created, 6 landed manually" and the web sidebar line read "4 done". `.ycc/workloop.json` held 12 done tasks: 4 in `completed` (tasks that existed at start) and 8 with status done in `created` (0472-0477, 0485, 0488). The headline undercounted finished work by a factor of three.
- "11 sessions" counted finished sessions only; 5 more were live (4 working, 1 waiting).
- Of the 6 "landed manually" commits, 5 were loop sessions' own hand-scoped `git commit`s (subjects such as "0474: ingest.go … (this task's hunks only; 0425's uncommitted … hunks stay in the working tree)") and 1 was a watch's notes commit. The label reads as commits the user made. The cause of the hand-scoped commits is 0593 (candidate tree for shared worktrees, proposed); this task changes only the wording and counts.

## Acceptance criteria
- The headline and the web sidebar loop line count every task that reached done during the loop, whether it existed at start or was created during it; tasks created and still open are counted separately.
- The session count distinguishes finished from live sessions (for example "11 finished, 5 live"), or the live ones are named on the slots line next to it.
- The manual-landings count and section use wording that does not imply the user made the commits (for example "other commits during the loop"), in web, iOS, the loop digest and the completion notification. The section's explanatory line stays.
- Created tasks that no session worked do not show "0 tokens · unpriced".
- Spec §9.1 wording follows if the digest terms change. Web vitest and YccKit tests cover the new summary; the iOS change is built and screenshot-checked on the Mac simulator.

## Work log
