---
id: "0611"
title: 'iOS work-loop screen: Stop in the toolbar, live sessions first, rendered markdown, task-headed rows; Live sessions post form last'
status: done
priority: 3
created: "2026-11-25"
updated: "2026-11-25"
depends_on:
    - "0605"
    - "0603"
spec_refs:
    - spec.md#9.1
---

## Description

Split from 0607 on 2026-11-25 (accepted by the user 2026-11-24 as part of 0607). These items edit `clients/ios/App/WorkLoopView.swift` and the iOS Live sessions screen (`CoordinationViews.swift`), which 0605 and 0603 were changing in parallel; 0607 kept the project-home loop line and the session header task ids.

Observed 2026-11-24 on wheeler loop `loop_d074bc58` (see 0607 for the full review):

- "Stop loop" is the last row of `WorkLoopView`, about nine screens below the top. Web has it in the page header.
- "Resource envelope" (four rows, all "unbounded") sits above "Live sessions".
- "Sessions run" summaries and "Latest evidence" show raw markdown. Web renders the same text as markdown.
- "Sessions run" rows are headed by a session id prefix and "attempt 1"; the task title is absent.
- "Created" rows show no status, where web shows in progress / blocked / done.
- The Live sessions screen opens with the "Post a notice" form, which fills the top half before any notice or session.

## Acceptance criteria

- Stop is reachable without scrolling while a loop runs (toolbar or pinned), with the existing confirmation.
- Live sessions come before the resource envelope; the envelope collapses to one line when every limit is unbounded.
- Session summaries and evidence render markdown.
- Sessions-run rows lead with the task id and title; the session id is secondary. "attempt 1" is shown only when a task had more than one attempt.
- Created rows show status.
- On Live sessions, notices and sessions come before the post form, or the form is collapsed until asked for.
- Built and screenshot-checked on the Mac simulator in light and dark; YccKit `swift test` passes for any model change.

## Outcome

Stop moved to the toolbar with the existing confirmation; it shows a disabled "Stopping…" while the loop stops. The resource envelope now comes after the live sessions and digest, and it is one expandable line when every limit is unbounded. Sessions-run rows lead with the canonical task id and title, with the session id secondary. "attempt N" shows only for tasks attempted more than once (WorkLoopModel.taskTitle/attemptLabel). Reports render as markdown, clamped with Show all; digest evidence sits in a markdown disclosure. Digest rows show a status pill when it differs from the section. The Live sessions post form now comes last. Built on the Mac; YccKit swift test passed (697 tests). Light and dark simulator screenshots were checked. sol accepted in round 3.

Commit: 0611: iOS work-loop screen — Stop in the toolbar, envelope last and collapsed, task-headed session rows, markdown reports, digest status; Live sessions post form last
