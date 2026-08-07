---
id: "0282"
title: Ios kanban view should show backlog in most recent first order
status: done
priority: 3
created: "2026-08-06"
updated: "2026-08-07"
depends_on: []
spec_refs: []
---

## Description
I want to see the newest stuff up top (could maybe make sort order a changeable thing)

## Acceptance criteria

## Plan

Goal: iOS backlog (board lanes *and* list sections) shows newest tasks first by default, with a user-changeable sort order that persists.

Ordering key: `Ycc_V1_BacklogTaskSummary` has no timestamp (proto: id/title/status/priority/depends_on/ready/blocked_by), and the daemon returns tasks sorted by id ascending. Backlog ids are allocated monotonically, so "most recent first" = **id descending**. Do this purely client-side — no proto/daemon change.

1. YccKit — `BacklogModel.swift`:
   - Add `public enum BacklogSort: String, Sendable, CaseIterable, Identifiable { case newestFirst, oldestFirst, priority }` with a `title` ("Newest first" / "Oldest first" / "Priority") and `id`.
   - Add a pure comparator, e.g. `public static func sorted(_ tasks: [Ycc_V1_BacklogTaskSummary], by sort: BacklogSort) -> [...]`:
     - newestFirst: id descending; oldestFirst: id ascending. Compare numerically when both ids parse as Int, else lexicographically (ids are zero-padded strings, but be robust to odd ids); make the order total/stable (tie-break on id string).
     - priority: priority ascending (1 = highest first), priority 0/unset sorts last, tie-break newest-first (id descending).
   - Add `public var sort: BacklogSort = .newestFirst` to `BacklogModel`; `sections` and `board` computed properties pass it through.
   - Give the static `sections(from:)` / `board(from:)` a `sort: BacklogSort = .newestFirst` parameter and apply the ordering within each section/lane. Update the doc comments (they currently claim "tasks keep their incoming order (the daemon sorts by id ascending)").
2. App — `BacklogView.swift`: add a toolbar sort control (a `Menu` labelled `arrow.up.arrow.down` containing a `Picker` bound to the sort) next to the existing presentation toggle; keep the existing board/list toggle button unchanged. Persist the choice with `@AppStorage("backlog.sort")` (String-raw-value enum, default `.newestFirst`), applying it to the model when the model is created and on change. Do not otherwise change board/list behaviour (task 0277 — default focus on the todo column — is a separate task; don't touch it).
3. Tests — `clients/ios/YccKit/Tests/YccKitTests/BacklogModelTests.swift`: cover newest-first default (lane and section contents come back id-descending), oldest-first, priority sort incl. priority-0-last and tie-break, non-numeric/odd ids not crashing and ordering deterministically, and that existing lane-shape expectations (empty lanes kept, unknown lane appended only when used) still hold. Update the existing `testBoardGroupsTasksIntoTheirLane` expectation to the new default order.
4. Docs — `docs/design/ios-client.md` (§ around line 338, the board description): one or two sentences that cards within a lane / rows within a section are ordered newest-first by default (id descending) and that the order is user-selectable and remembered.

Verification: Swift can't be built on this Linux workspace (no toolchain) — the implementer should self-review carefully for compile correctness (SwiftUI `@AppStorage` with a `String`-RawRepresentable enum, `@Observable` model mutation from the view) and note in its report that `cd clients/ios/YccKit && swift test` plus an xcodebuild of the app must be run by the user on the Mac. Keep the change tight and idiomatic with the surrounding code.

## Work log
- 2026-08-07 plan: Goal: iOS backlog (board lanes *and* list sections) shows newest tasks first by default, with a user-changeable sort order that persists.  Ordering key: `Ycc_V1_BacklogTaskSummary` has no timestamp (p
…[truncated]
- 2026-08-07 implementer report: Implemented Task 0282 across the iOS client: - Added public `BacklogSort` options (`newestFirst`, `oldestFirst`, `priority`) and deterministic client-side sorting in `BacklogModel`. - Board lanes and 
…[truncated]
- 2026-08-07 review tier: simple (coordinator self-review)
- 2026-08-07 revision: Fixed the reviewer-identified comparator issue: - `compareIDs` now defines a strict total order: numeric ids first in numeric order, numeric ties by raw string, then non-numeric ids lexicographically.
…[truncated]
- 2026-08-07 decision: accept — commit: ios: order backlog newest-first with a persisted sort control (task 0282)
