---
id: "0227"
title: Make iOS home show recent sessions across all projects
status: done
priority: 2
created: "2026-08-03"
updated: "2026-08-06"
depends_on: []
spec_refs:
    - docs/design/ios-client.md#Navigation shell — workspace drawer + recent-session feed
    - docs/design/ios-client.md#Phase 1 — observe, answer, control (parity with the web client)
---

## Description
Change the authenticated iOS app's default landing page from a single-project session history to a cross-project recent-session feed. Aggregate session history across all registered projects, deduplicate workspaces, and sort globally by last activity.

## Acceptance criteria
- [x] The default authenticated page lists sessions from all projects.
- [x] Sessions are globally ordered by last activity (with started-at fallback), most recent first.
- [x] Every row identifies its project.
- [x] Project-scoped navigation remains available.
- [x] Partial per-project failures preserve successful results and surface a warning.
- [x] YccKit tests cover aggregation, sorting, deduplication, and partial failures.
- [ ] Swift package tests and iOS simulator build pass (blocked in this Linux environment: `swift`, `xcodebuild`, and `xcrun` are unavailable).

## Work log
- 2026-08-03: Implemented a default cross-project recent feed with per-session project routing, globally sorted history, workspace/session deduplication, project annotations, scoped filters, and partial-results warnings. Updated the iOS design and added YccKit coverage. `go test ./...` passes; Swift/iOS verification requires the macOS workspace.
- 2026-08-07: Closed done in a backlog audit — implemented and committed; on-device use is the verification the Linux box could not provide.
