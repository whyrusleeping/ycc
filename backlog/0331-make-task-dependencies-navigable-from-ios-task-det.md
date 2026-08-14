---
id: "0331"
title: Make task dependencies navigable from iOS task detail
status: in_review
priority: 2
created: "2026-08-14"
updated: "2026-08-14"
depends_on: []
spec_refs:
    - docs/design/ios-client.md#Navigation and interaction
---

## Description
On iOS, task detail shows `Blocked by` and `Depends on` task IDs as inert text, leaving no route to the blocking task. Make dependency references actionable and open the referenced task detail in the same project through the shared home router.

## Acceptance criteria
- Every blocking/dependency task ID shown on task detail can be tapped to open that task.
- Navigation uses `HomeRouter.open` so task-to-task cycles deduplicate rather than growing the navigation stack indefinitely.
- The link remains understandable and accessible as a task navigation action.

## Work log

- Made each `blocked_by` and `depends_on` ID an explicit task-detail button with a disclosure indicator and VoiceOver description.
- Routed links through `HomeRouter.open` in the current project so dependency cycles reuse existing screens.
- Updated the iOS navigation design contract. `git diff --check` passes; Swift/Xcode verification is unavailable in this environment and remains for on-device review.
