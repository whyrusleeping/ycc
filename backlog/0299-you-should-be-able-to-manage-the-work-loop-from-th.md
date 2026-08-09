---
id: "0299"
title: You should be able to manage the work loop from the backlog kanban view
status: in_review
priority: 3
created: "2026-08-08"
updated: "2026-08-09"
depends_on: []
spec_refs: []
---

## Description

## Acceptance criteria

## Plan

Surface work-loop management inside the iOS backlog (kanban) screen so the user can start, observe, and stop the daemon-owned loop without leaving the board. iOS-only; no proto/daemon changes (StartWorkLoop/StopWorkLoop/GetWorkLoop + WorkLoopModel already exist). Per iOS convention the task ends in_review with changes left UNCOMMITTED (the tree already carries a large pile of uncommitted in_review iOS work).

1. YccKit — WorkLoopModel.swift: add a pure static helper for a compact banner line, headless-testable:
   - `static func bannerLine(for loop: Ycc_V1_WorkLoopInfo?) -> String` — nil loop → "Not running"; running → "Running · N sessions" (reuse summaryLine parts or a shorter form); waiting → reuse waitingLine (kind + resume time); stopping → "Stopping…"; finished → "Finished · <summaryLine>". Keep composition simple and deterministic for tests.
2. App — BacklogView.swift:
   - When the backlog is scoped to a concrete project (model.selectedProject non-empty), create a WorkLoopModel for that project (rebuild when the project filter changes — key the state/task on the selected project).
   - Render a compact work-loop banner ABOVE the board/list (top placement; avoid bottom chrome per FB13296535 keyboard gotcha): state badge (reuse WorkLoopStateBadge — make it internal instead of private in WorkLoopView.swift), the bannerLine text, and controls:
     * Start button when state.canStart — behind the same confirmation dialog copy as WorkLoopView ("The daemon will drain ready backlog tasks unattended… can spend tokens…").
     * Stop button when state.isActive — destructive, with confirmation (waiting-state copy variant included).
     * Tapping the banner itself navigates to the full work-loop page: router.open(.workLoop(project:)) (HomeRouter already has the destination).
   - Poll the loop snapshot while active using the same `.task(id: shouldPoll)` + 5s sleep pattern as WorkLoopView; on each poll, if sessionsRun (or state) changed since the last snapshot, also `await model.refresh()` the backlog so lanes reflect loop progress.
   - Surface actionError via an alert (same pattern as WorkLoopView). Hide the banner entirely when selectedProject is empty (multi-project unfiltered view) or the client is unavailable.
3. Tests — YccKit WorkLoopModelTests.swift: cover bannerLine for nil/running/waiting/finished/stopping snapshots.

Constraints: no Swift toolchain here — write carefully; `swift test` runs on the user's Mac. Do not commit anything (the `commit` tool stages -A and the tree holds many other tasks' uncommitted work); end in_review.

### Starting points
- clients/ios/App/BacklogView.swift — content()/toolbar; BacklogBoard; model.selectedProject
- clients/ios/App/WorkLoopView.swift — WorkLoopStateBadge (private, ~313), confirmation dialog copy (~28-57), loadAndPoll pattern (~295)
- clients/ios/YccKit/Sources/YccKit/WorkLoopModel.swift — WorkLoopState gates, summaryLine/waitingLine helpers
- clients/ios/App/HomeRouter.swift — HomeDestination.workLoop(project:) exists; router.open
- clients/ios/YccKit/Tests/YccKitTests/WorkLoopModelTests.swift — existing loop(...) builder + test patterns

## Work log
- 2026-08-09 plan: Surface work-loop management inside the iOS backlog (kanban) screen so the user can start, observe, and stop the daemon-owned loop without leaving the board. iOS-only; no proto/daemon changes (StartWo
…[truncated]
- 2026-08-09 context hints: 5 recorded with plan
- 2026-08-09 context hints: clients/ios/App/WorkLoopView.swift — confirmation dialog copy lines 28-57; loadAndPoll lines 295-310; WorkLoopStateBadge line 313 (private → internal); clients/ios/App/HomeRouter.swift — HomeDes
…[truncated]
- 2026-08-09 preload: 3 file(s), ~48 KiB seeded into implementer context
- 2026-08-09 implementer report: Implemented task 0299 without committing or staging changes.  Changes: - Added `WorkLoopModel.bannerLine(for:)` with deterministic nil/running/waiting/stopping/finished/unknown-state composition. - Ma
…[truncated]
- 2026-08-09 review tier: single-opus — reviewers: sol
- 2026-08-09 review (sol): revise — The banner helper, controls, navigation, project scoping, state badge reuse, and requested helper tests are implemented cleanly, but the polling lifecycle has a cancellation race at the most important
…[truncated]
- 2026-08-09 revision: Fixed the completion-transition race in `BacklogView.swift` only.  - Added persistent `@State` snapshot tracking keyed by project, loop state, and `sessionsRun`. - Recorded the snapshot after every in
…[truncated]
- 2026-08-09 review (sol): accept — The revision resolves the blocking completion race: loop snapshot state is now retained across `.task(id:)` restarts, and changed snapshots launch the backlog refresh in an unstructured task that is n
…[truncated]
- 2026-08-09 usage: 2,096,941 tok (in 988,714, out 30,211, cache_r 2,231,513, cache_w 41,288) · cost n/a (unpriced)
  reviewer:sol: 1,069,512 tok (in 606,524, out 9,612, cache_r 453,376, cache_w 0) · cost n/a (unpriced)
  implementer: 1,019,106 tok (in 382,164, out 12,302, cache_r 624,640, cache_w 0) · cost n/a (unpriced)
  coordinator: 8,323 tok (in 26, out 8,297, cache_r 1,153,497, cache_w 41,288) · cost n/a (unpriced)
