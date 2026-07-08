---
id: "0181"
title: 'iOS: session list — history, project filter, needs-answer badges'
status: todo
priority: 2
created: "2026-07-08"
updated: "2026-07-08"
depends_on:
    - "0180"
spec_refs:
    - docs/design/ios-client.md#6. Screens & feature phases
---

## Description
Session list screen per `docs/design/ios-client.md` §6 phase 1 step 2.

## Description
- List from `ListSessionHistory` (live + persisted, most-recent-first); project filter from `ListProjects` (hidden when ≤1 project); optional `project` param re-query.
- Per row: title, status badge (running/idle/error), live marker, turns, relative last-activity time. `waitingInput:true` rows styled loudest ("needs answer") and sorted/sectioned to the top.
- Pull-to-refresh and refresh on scenePhase → active.
- Navigation: tapping a row pushes the session view (0182's destination; a stub detail is fine until it lands if this task merges first).

## Acceptance criteria
- Rows render all listed fields; protojson quirks handled by the generated client (int64 as strings decoded to Int64).
- Needs-answer sessions are visually unmistakable and listed first.
- Project filter round-trips; empty daemon shows a sane empty state.
- YccKit view-model logic (sorting/sectioning/filtering) covered by `swift test`.

## Work log
