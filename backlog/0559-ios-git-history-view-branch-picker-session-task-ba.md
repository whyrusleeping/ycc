---
id: "0559"
title: 'iOS: git history view (branch picker, session/task badges, diff drill-in)'
status: todo
priority: 3
created: "2026-11-17"
updated: "2026-11-17"
depends_on:
    - "0558"
spec_refs:
    - docs/design/ios-client.md
---

## Description
Status: todo, blocked on 0558 (ListCommits RPC + web History page). Requested by the user 2026-11-15 in the same session as 0558. iOS is a separate task because it must be built and checked on the configured Mac and then waits `in_review` for on-device use.

## Scope
- A History entry in the project drawer pushes a commit list for the project, with a branch picker fed by `ListCommits`' branch list (workstream branches marked).
- Rows show subject, short sha, author, relative time, and +/- stats. Commits made by ycc sessions show a session/task badge that opens the session or task.
- Tapping a commit opens the existing `DiffView` (`GetCommitDiff`). Scrolling to the end loads the next page.
- Per-file history is out of scope (deferred by the user).

## Acceptance criteria
- YccKit client method plus paging/model logic covered by `swift test`.
- The app builds and its tests pass on the Mac, with simulator screenshots of the list and the diff drill-in against the ios-review daemon.

## Work log
