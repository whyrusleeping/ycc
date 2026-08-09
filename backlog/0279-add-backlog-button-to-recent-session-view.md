---
id: "0279"
title: Add backlog button to recent session view
status: in_review
priority: 3
created: "2026-08-06"
updated: "2026-08-09"
depends_on: []
spec_refs: []
---

## Description
Just have a project picker modal when clicking into the backlog from the non-specific session list view (in ios app)

## Acceptance criteria

## Plan

Make the Backlog toolbar button available from the unscoped "Recent" session list in the iOS app, asking which project's backlog to open via a picker modal (it currently only appears when a project is selected in the drawer).

Changes (all in clients/ios/App/LandingView.swift, mirroring the existing new-chat project picker pattern):
1. Add `@State private var showBacklogProjectPicker = false`.
2. In `projectDestinations`, make the Backlog ToolbarItem unconditional:
   - When `model?.selectedProject` is non-nil: keep the existing `NavigationLink(value: .backlog(project:))`.
   - When unscoped: a Button that (a) with >1 registered project sets `showBacklogProjectPicker = true`; (b) with exactly one project opens its backlog directly via `router.open(.backlog(project:))`; (c) with zero projects is disabled (BacklogView needs a project).
3. Add a `confirmationDialog("Open backlog for…", isPresented: $showBacklogProjectPicker, titleVisibility: .visible)` in `presentations(_:)` alongside the new-chat one, listing `newSessionProjectChoices(model)` (this helper already hoists the last-viewed project to the top) with each button calling `router.open(.backlog(project: project))`, plus a Cancel button.
4. Reuse the existing `newSessionProjectChoices(_:)` helper for ordering rather than duplicating logic; if renaming it to something generic (e.g. `projectChoices`) keep the new-chat call sites updated. Purely optional — reuse as-is is fine.

Verification: no Swift toolchain in this environment — careful read-through + reviewer pass; on-device verification by the user (task ends at in_review per project convention, change stays uncommitted like other pending iOS work).

### Starting points
- clients/ios/App/LandingView.swift — projectDestinations (~line 329), presentations(_:) (~line 74, new-chat confirmationDialog pattern), beginNewSession()/newSessionProjectChoices() (~line 409)
- HomeRouter destination: HomeDestination.backlog(project:) — open via router.open(...)
- SessionListModel.newSessionProjectChoices / requiresProjectChoiceForNewSession (clients/ios/YccKit/Sources/YccKit/SessionListModel.swift ~line 160)
- Tree holds other tasks' uncommitted iOS work — keep the diff tight to this change; do not reformat unrelated code

## Work log
- 2026-08-09 plan: Make the Backlog toolbar button available from the unscoped "Recent" session list in the iOS app, asking which project's backlog to open via a picker modal (it currently only appears when a project is
…[truncated]
- 2026-08-09 context hints: 4 recorded with plan
- 2026-08-09 context hints: clients/ios/App/LandingView.swift: presentations(_:) ~line 74 (new-chat confirmationDialog), projectDestinations ~line 329 (current conditional Backlog ToolbarItem), beginNewSession() ~line 409, newSe
…[truncated]
- 2026-08-09 preload: 1 file(s), ~42 KiB seeded into implementer context
- 2026-08-09 implementer report: Implemented task 0279 in `clients/ios/App/LandingView.swift` only. The Backlog toolbar button now appears on the unscoped Recent list: it opens the sole registered project directly, presents an ordere
…[truncated]
- 2026-08-09 review tier: single-opus — reviewers: sol
- 2026-08-09 review (sol): accept — The LandingView change satisfies the task as planned. The Backlog toolbar item is now available on the unscoped Recent view, preserves direct project-scoped navigation, opens the sole registered proje
…[truncated]
- 2026-08-09 usage: 304,491 tok (in 214,178, out 8,137, cache_r 450,643, cache_w 14,766) · cost n/a (unpriced)
  implementer: 224,482 tok (in 153,212, out 2,150, cache_r 69,120, cache_w 0) · cost n/a (unpriced)
  reviewer:sol: 75,607 tok (in 60,950, out 1,601, cache_r 13,056, cache_w 0) · cost n/a (unpriced)
  coordinator: 4,402 tok (in 16, out 4,386, cache_r 368,467, cache_w 14,766) · cost n/a (unpriced)
