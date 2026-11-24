---
id: "0607"
title: 'iOS work-loop screen: Stop in the toolbar, live sessions first, rendered summaries, task-headed rows, loop status on the project home'
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

## Acceptance criteria
- Stop is reachable without scrolling while a loop runs (toolbar or pinned), with the existing confirmation.
- Live sessions come before the resource envelope; the envelope collapses to one line when every limit is unbounded.
- Session summaries and evidence render markdown.
- Sessions-run rows lead with the task id and title; the session id is secondary. "attempt 1" is shown only when a task had more than one attempt.
- Created rows show status.
- A project's home shows a compact loop status line while a loop runs, opening the loop screen.
- The session header shows the focused task id(s).
- On Live sessions, notices and sessions come before the post form, or the form is collapsed until asked for.
- Built and screenshot-checked on the Mac simulator in light and dark; YccKit `swift test` passes for any model change.

## Work log
