---
id: "0332"
title: 'TUI: make persistent remote multi-project mode a first-class workspace hub'
status: in_review
priority: 2
created: "2026-08-14"
updated: "2026-08-14"
depends_on: []
spec_refs:
    - Daemon lifecycle and projects
    - Client surfaces
    - docs/design/ios-client.md#Navigation and interaction
---

## Description
Bring the TUI's persistent/remote-daemon experience up to the interaction model proven in the iOS client. Replace the one-time, local-cwd-oriented project picker with a reusable daemon-scoped workspace hub: make project switching reachable from home, use daemon-owned project/git data, provide a server-side directory browser for AddProject, and expose safe rename/remove actions. Project changes must clear stale project-scoped projections before refreshing so state from one workspace is never shown under another.

## Acceptance criteria

- [x] A persistent/remote TUI can return to the project hub from home and switch projects without restarting.
- [x] Adding a project browses the daemon host through ListDir; it never sends the TUI process's local cwd as though it were a daemon-host path.
- [x] Projects can be renamed and removed with explicit input/confirmation; removing the active project returns to the hub.
- [x] The selected project's daemon-reported GitStatus drives remote-mode home context; the client does not shell out in a remote workspace path.
- [x] Switching projects resets and refreshes project-scoped backlog, history/last-session, waiting-session, usage, work-loop, and git state.
- [x] Focused tests cover remote add/navigation, switching/reset behavior, project management, and daemon git projection.

## Work log

- 2026-08-14: Implemented the reusable project hub, ListDir-backed daemon filesystem browser, rename/remove flows, remote git projection, scope-generation guards, and subscription detachment so switching projects never stops daemon-owned sessions. `go test ./...` passes.
