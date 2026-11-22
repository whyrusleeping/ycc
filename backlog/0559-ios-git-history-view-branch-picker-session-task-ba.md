---
id: "0559"
title: 'iOS: git history view (branch picker, session/task badges, diff drill-in)'
status: in_review
priority: 3
created: "2026-11-17"
updated: "2026-11-17"
depends_on:
    - "0558"
spec_refs:
    - docs/design/ios-client.md
---

## Description
Status: in review. It was implemented in `9d3b011`, and what remains is a check on a device. 0558 (ListCommits RPC + web History page) is done. Requested by the user on 2026-11-17 in session s_86ed638051f9e53b, the same session as 0558 (event 2; scope answers at event 35). iOS is a separate task because it must be built and checked on the configured Mac and then waits `in_review` for on-device use.

## Scope
- A History entry in the project drawer pushes a commit list for the project, with a branch picker fed by `ListCommits`' branch list (workstream branches marked).
- Rows show subject, short sha, author, relative time, and +/- stats. Commits made by ycc sessions show a session/task badge that opens the session or task.
- Tapping a commit opens the existing `DiffView` (`GetCommitDiff`). Scrolling to the end loads the next page.
- Per-file history is out of scope (deferred by the user).

## Acceptance criteria
- YccKit client method plus paging/model logic covered by `swift test`.
- The app builds and its tests pass on the Mac, with simulator screenshots of the list and the diff drill-in against the ios-review daemon.

## Work log

- 2026-11-17: Implemented and committed. History sits in the project overflow menu. It uses `CommitHistoryModel` (paging, dedupe, a branch picker that sends the checked-out branch as HEAD, and dropping stale responses) and `CommitHistoryView` (a branch bar, rows with +/- stats, task and session badges). Rows push `DiffView` through `HomeDestination.commitDiff`. Results: `swift test` passed (640 tests), and `xcodebuild test` passed on ycc-review-A. Simulator checks covered the list, the branch picker, a workstream branch, the diff, the task and session badges, and scroll paging to row 243. They used the 0558 history scratch daemon on hist-proj rather than ios-review, because ios-review has no commits linked to sessions. Remaining step: check it on a device.
- 2026-11-21 groom: replaced the stale "todo, blocked on 0558" status line with the in_review state. Corrected the request date from 2026-11-15 to 2026-11-17, which is when session s_86ed638051f9e53b began and created this task at event 76. Added the commit hash.
