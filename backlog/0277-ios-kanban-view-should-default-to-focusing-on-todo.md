---
id: "0277"
title: Ios kanban view should default to focusing on todo column
status: in_review
priority: 3
created: "2026-08-06"
updated: "2026-08-09"
depends_on: []
spec_refs: []
---

## Description
Keep proposed on the left, but show the right side of cards on the left edge as a visual indication that theres something over there so the user knows to swipe

## Acceptance criteria

## Plan

Make the iOS backlog board open focused on the **todo** lane while keeping **proposed** leftmost, with a sliver of the proposed cards peeking in on the left edge so the user can tell there is a lane to swipe back to.

All changes confined to clients/ios/App/BacklogView.swift (currently clean; the working tree holds other in_review tasks' uncommitted work which must not be touched).

1. `BacklogBoard`:
   - Add `@State private var focusedLane: String? = TaskStatus.todo.rawValue` (BacklogSection.id == status.rawValue, and the board always keeps empty lanes, so the todo lane always exists).
   - Attach `.scrollPosition(id: $focusedLane, anchor: .leading)` (iOS 17 API) to the horizontal ScrollView; ForEach identity inside `.scrollTargetLayout()` supplies the lane ids. This makes the board start on the todo column and keeps tracking the user's swipes thereafter.
2. Peek affordance: split the symmetric `.contentMargins(.horizontal, 12, …)` into a larger leading margin (e.g. 28–32pt leading, 12pt trailing). With the 12pt lane spacing, the previous lane's trailing ~16–20pt shows on the left edge whenever a non-first lane is snapped — the "right side of cards on the left edge" hint. When the proposed lane itself is snapped, the extra leading inset is harmless.
3. Update the doc comments on BacklogBoard to describe the default-focus + peek behavior.

No YccKit model changes, so no new headless tests; no Swift toolchain here — verification is `xcodebuild`/on-device on the user's Mac, so the task ends in_review and UNCOMMITTED per the project's iOS convention (the `commit` tool does `git add -A` and would sweep other tasks' work anyway).

### Starting points
- clients/ios/App/BacklogView.swift — BacklogBoard struct (~line 263): horizontal ScrollView + LazyHStack + .scrollTargetLayout + .viewAligned + .contentMargins(.horizontal, 12)
- clients/ios/YccKit/Sources/YccKit/BacklogModel.swift — BacklogSection.id = status.rawValue; boardColumns keeps empty lanes
- Only BacklogView.swift may change — every other dirty file in git status belongs to other in_review tasks

## Work log
- 2026-08-09 plan: Make the iOS backlog board open focused on the **todo** lane while keeping **proposed** leftmost, with a sliver of the proposed cards peeking in on the left edge so the user can tell there is a lane t
…[truncated]
- 2026-08-09 context hints: 3 recorded with plan
- 2026-08-09 context hints: clients/ios/App/BacklogView.swift lines 258-295: BacklogBoard — ScrollView(.horizontal) > LazyHStack > ForEach(model.board) { BacklogLane }, .scrollTargetLayout(), .contentMargins, .scrollTargetBeha
…[truncated]
- 2026-08-09 preload: 1 file(s), ~6 KiB seeded into implementer context
- 2026-08-09 implementer report: Implemented Task 0277 in `clients/ios/App/BacklogView.swift` only: - Added `focusedLane`, initially set to `TaskStatus.todo.rawValue`. - Bound the horizontal board to that lane using `.scrollPosition(
…[truncated]
- 2026-08-09 review tier: simple (coordinator self-review)
- 2026-08-09 usage: 82,005 tok (in 38,703, out 7,974, cache_r 291,682, cache_w 16,499) · cost n/a (unpriced)
  implementer: 75,060 tok (in 38,691, out 1,041, cache_r 35,328, cache_w 0) · cost n/a (unpriced)
  coordinator: 6,945 tok (in 12, out 6,933, cache_r 256,354, cache_w 16,499) · cost n/a (unpriced)
