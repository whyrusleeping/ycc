---
id: "0245"
title: 'iOS: kanban board for the backlog + backlog shortcut from a session'
status: in_review
priority: 2
created: "2026-08-05"
updated: "2026-08-05"
depends_on: []
spec_refs: []
---

## Description

Two follow-ups from device review of the navigation rework (task 0214).

**1. Backlog unreachable from a session (regression).** Moving the project destinations into the workspace drawer made them unreachable from any *pushed* screen: the hamburger lives only on the root view's toolbar, and the drawer's left-edge reveal is deliberately disabled while a screen is pushed so it cannot fight the system's interactive back gesture. From a session — the screen users spend most of their time in — checking the backlog meant navigating back to the root first.

**2. The backlog did not feel like the kanban it was gesturing at.** It was a vertical `List` of status sections: the same information, but none of the spatial, lane-based feel that makes a board useful.

## Acceptance criteria

- [x] The session view offers a one-tap Backlog link scoped to its project, with Workstreams and Usage in its overflow menu.
- [x] The session view's stream state moves into the navigation subtitle, so adding the shortcut costs no toolbar slot.
- [x] The backlog defaults to a board of horizontally snapping lanes in workflow order, with empty lanes retained.
- [x] Board lane order is workflow order (proposed → todo → in progress → in review → blocked → done), distinct from the list's "active first" section order.
- [x] Cards show id, priority, title, and readiness, and carry a discoverable move menu offering workflow neighbours first.
- [x] The compact list remains available via a toolbar toggle, and the choice persists.
- [x] Board/lane logic is unit-tested in YccKit.
- [ ] Verified on device: lane snapping, nested vertical scrolling inside lanes, and pull-to-refresh within a lane.

## Work log
- `TaskStatus` gained `boardOrder`, `boardColumns`, and `previousBoardColumn` / `nextBoardColumn`; `BacklogModel.board(from:)` groups into lanes keeping empties, appending an `unknown` lane only when tasks actually carry an unrecognised status. Five new `BacklogModelTests`.
- `BacklogView` rebuilt: `BacklogBoard` (horizontal `LazyHStack` + `.scrollTargetLayout()` / `.scrollTargetBehavior(.viewAligned)`, lanes at 82% of container width so the next lane peeks), `BacklogLane` (status pill + count header, vertical card scroll with `.refreshable`, dashed placeholder when empty), `BacklogCard` (id/priority/title/readiness plus a move menu that is its own hit target). List presentation retained behind an `@AppStorage` toggle.
- Content insets use `.contentMargins(_:for: .scrollContent)` rather than padding, so `.scrollTargetLayout()` sits directly on the layout container and lane snapping resolves correctly.
- `SessionView`: backlog toolbar link, workstreams/usage in the overflow menu, connection state folded into the navigation subtitle.
- Documented both in docs/design/ios-client.md §6, including *why* the drawer is not reachable from pushed screens and how per-screen shortcuts compensate.
- NOT YET VERIFIED on device.
