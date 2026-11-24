---
id: "0605"
title: 'Parallel-aware work-loop view: live slots as cards with task, intent and time in state (web + iOS)'
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

The loop views were shaped for one session at a time. Observed 2026-11-24 on wheeler loop `loop_d074bc58` (4 slots, 4 working and 1 waiting):

- Web loop page: "Now running s_f55cbb77df1b15ad · Follow live session →" (`WorkLoopPage.tsx` around line 443) names one slot's session. The "Live sessions" table below lists raw session ids, a task id and working/waiting, with no task title, intent, time in state or last activity. iOS lists the same fields.
- The Live sessions page (`/p/<project>/live`) already shows each session's intent, held tasks and last activity; the loop page shows none of it.
- Slot 3 (task 0470) had shown "working" for 3h20m while its transcript sat in `wait job_6` on a backfill expected to take two hours. Nothing on the loop page distinguished it from an active session. 0d19031 (10-minute cap on a loop session's wait on its own shell jobs) changes the daemon side; the view still needs the time.
- Web sidebar loop line: "Work loop 4 done / 0272 Work on the backlog as one sess…", one session's title, instead of the slots line.
- Stop confirmation copy is singular in both clients: "The current session will finish. The daemon will not pick another backlog task." (`clients/ios/App/WorkLoopView.swift:556`, `clients/web/src/features/workloop/WorkLoopPage.tsx:209`). In a parallel loop several working sessions finish and waiting sessions keep running (spec §9.1).
- Web session header clips its metadata when the title is long ("up 3h :").

iOS layout items outside the live-slot rows (Stop placement, section order, markdown) are in 0607 (iOS work-loop screen pass, filed from the same review).

## Acceptance criteria
- Web and iOS loop views show each live slot as a row or card with the task id and title, the session's intent when set, working/waiting, how long it has been in that state, and its last activity time. The session and the task both open from it.
- The single-session "Now running … Follow live session" line is gone when the loop has more than one slot.
- The web sidebar loop line shows "k/N working, m waiting" for a parallel loop.
- The stop confirmation in both clients states how many working sessions will finish and that waiting sessions keep running, using the snapshot's slots.
- The web session header does not clip metadata mid-token with a long title.
- If the snapshot lacks a needed field (intent, last activity), the proto change and its test are part of this task.
- Web typecheck and vitest pass; iOS is built and screenshot-checked on the Mac simulator against a daemon running a loop with parallel > 1.

## Work log
