---
id: "0584"
title: iOS surfaces for session awareness, the notice board, and the coordination setting
status: done
priority: 4
created: "2026-11-22"
updated: "2026-11-22"
depends_on:
    - "0582"
spec_refs:
    - spec.md#7.3
    - spec.md#18.5
---

## Description

Current state: todo; its dependency 0582 (daemon API and CLI for the project sessions view: `ListProjectNotices.sessions`, `PostProjectNotice`) is done. Delivers the iOS outcome of 0562 (client surfaces for session awareness and the notice board). Separate from the web work because it needs the configured Mac build and simulator screenshots.

## Acceptance criteria

- Built and tested on the configured Mac (`xcodebuild` build/test, `swift test` for YccKit model changes), with simulator screenshots of each surface.

## Outcome

iOS session coordination surfaces: Live sessions screen (project overflow menu while on; sessions with activity, intent, held tasks, claimed paths; notices with post and clear-with-reason; off state pointing to Settings), held badges on backlog cards/rows and task detail that open the holding session, active notices in the question sheet, folded coordination_notice rows (overlap and board) with session-id links, and Settings › Session coordination per-project toggles via SetProjectSettings. Pushed sessions now have a per-id view identity, so a session-to-session link shows the new transcript. Verified on the Mac: swift test 680/680, xcodebuild build and test pass, with light and dark simulator screenshots of each surface against a scratch daemon. Review (sol) accepted in round 2 after fixes for stale-cache badge gating, a Settings read/write race, and detail loading for abbreviated rows.

Commit: iOS: live sessions view and notice board, held-task badges, question notices, coordination transcript rows, coordination setting (0584)
