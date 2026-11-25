---
id: "0607"
title: 'iOS: work-loop status line on the project home; focused task ids in the session header'
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

Observed 2026-11-24 with the HEAD iOS app on an iPhone 18 Pro simulator against wheeler loop `loop_d074bc58` (11 finished sessions, 5 live, 19 created tasks):

- "Stop loop" is the last row of `WorkLoopView`, about nine screens below the top. Web has it in the page header.
- "Resource envelope" (four rows, all "unbounded" on this loop) sits above "Live sessions".
- "Sessions run" summaries and "Latest evidence" show raw markdown (`**Why baraag.net failed.**`, backticked identifiers). Web renders the same text as markdown.
- "Sessions run" rows are headed by a session id prefix and "attempt 1" (`s_b6fe7f 0466 attempt 1`); the task title is absent.
- "Created" rows show no status, where web shows in progress / blocked / done.
- A project's home list has no loop indicator; the loop is reachable through More → Work loop. Only the Backlog screen has a loop banner.
- The session header reads "wheeler · opus · 57m · live" with no task id; the web header has task chips.
- The Live sessions screen opens with the "Post a notice" form, which fills the top half before any notice or session.

The live-slot rows and the stop confirmation copy belong to 0605 (parallel-aware work-loop view, web + iOS); the "0 tokens · unpriced" text on created rows belongs to 0604 (work-loop digest headline counts). Both were filed from the same review.

Split 2026-11-25: the `WorkLoopView` items (Stop placement, section order, markdown, sessions-run and created rows) and the Live sessions post form moved to 0611, because 0605 and 0603 were editing those screens in parallel. This task keeps the project-home loop line and the session header task ids.

## Acceptance criteria

- A project's home shows a compact loop status line while a loop runs, opening the loop screen.
- The session header shows the focused task id(s).
- Built and screenshot-checked on the Mac simulator in light and dark; YccKit `swift test` passes for any model change.

## Outcome

A project's iOS home shows a one-line work-loop status (state, slots, progress) while its loop is active, including on an empty project home, opening the loop screen; polled only while the home list is on screen. The session header names focused task ids ("wheeler · 0002 · mock · 7m live"), from the list row, navigation (search hits) and the new daemon SessionViewState.focus_tasks (sessionview fold, schema 11), so claims outside loaded pages show. WorkLoopView items and the Live sessions form split to 0611. Verified: go tests, web-build, Mac build, YccKit 696 tests, sim light/dark. sol accepted (round 5).

Commit: 0607: iOS project-home work-loop line; session header names focused tasks (SessionViewState.focus_tasks); split WorkLoopView items to 0611
