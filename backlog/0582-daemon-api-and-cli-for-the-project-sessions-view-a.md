---
id: "0582"
title: Daemon API and CLI for the project sessions view and user-posted notices
status: done
priority: 4
created: "2026-11-22"
updated: "2026-11-22"
depends_on: []
spec_refs:
    - spec.md#7.3
---

## Description
Current state: done. Delivers the shared daemon contract of 0562 (client surfaces for session awareness and the notice board) plus its terminal-client slice; 0583 (web) and 0584 (iOS) build on it.

## Acceptance
- Go tests cover the session listing (claims, canonical focus ids, gating off) and user posting/validation.
- The CLI shows the notice list and held badges against a scratch daemon.

## Outcome
`ListProjectNotices` returns `sessions` (`ProjectLiveSession`: activity code, held/earlier focus with canonical ids, title, worktree, intent, claims, running jobs) built by the `liveSessions` collector that `list_sessions` and the `list_backlog` footer now format from. `PostProjectNotice` posts as the user (expiry seconds range-checked before conversion). The CLI gained `ycc notices [list|post|clear]`, and `ycc task list` shows `[held by …]` instead of `[READY]` for held tasks. Spec §7.3, docs/remote-api.md and docs/cli.md are updated. Verified with `go test ./...` and a scratch daemon with a mock model (held badge, post, overflow rejection, clear, coordination off). It was committed from an isolated worktree with only this task's hunks, because the shared proto and `server.go` also held 0461's uncommitted hunks (user's choice, 2026-11-22). Generated Swift is unbuilt on macOS; 0584 builds it.
