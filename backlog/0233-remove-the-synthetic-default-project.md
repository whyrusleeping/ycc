---
id: "0233"
title: Remove the synthetic “Default” project
status: done
priority: 2
created: "2026-08-04"
updated: "2026-08-04"
depends_on: []
spec_refs:
    - Daemon lifecycle & projects
    - Persistence & remote sync
    - docs/design/ios-client.md
---

## Description
Eliminate the daemon-default workspace as a user-visible or routing concept. Every workspace, including a one-shot daemon's current directory, is represented by a named project. Project-scoped operations select a registered project rather than treating an empty project name as a hidden “Default” workspace.

## Acceptance criteria
- [x] One-shot daemons expose their current workspace as a normal named project.
- [x] Persistent daemons do not create or route to an implicit default workspace.
- [x] TUI, iOS, CLI, and remote API no longer present a synthetic “Default” project choice.
- [x] Empty/omitted project selection is handled only as an unambiguous sole-project convenience or rejected clearly when selection is required.
- [x] Specs/docs and tests describe the named-project model consistently.

## Work log
- 2026-08-04: Replaced the hidden daemon-default workspace with normal named project registration across the manager/daemon and all clients. Empty project routing now succeeds only for a sole project; multi-project omission returns a clear selection error. Regenerated Go/Swift protobufs and updated specs/docs/tests. `go test ./...` and `git diff --check` pass; Swift tooling is unavailable in this Linux workspace.
