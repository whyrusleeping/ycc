---
id: "0275"
title: Surface workspace git sync status (ahead/behind/dirty) in the app
status: todo
priority: 3
created: "2026-07-16"
updated: "2026-08-06"
depends_on: []
spec_refs:
    - Projects
---

## Description
Show, per registered project, whether its workspace checkout is up to date with its
remote — ahead/behind counts against the upstream tracking branch plus a dirty flag —
in the TUI home project list and the iOS app.

## Design

**Why cached + background fetch:** ahead/behind requires fresh remote refs, which means
`git fetch` — network I/O that can be slow or trigger auth prompts. `ListProjects` must
not fetch inline. A per-project background poller fetches periodically and caches the
result; `ListProjects` returns the cached snapshot instantly. Dirty + ahead/behind are
computed cheaply from local refs and are always fresh; only the remote ref refresh is
periodic.

## Layers

1. **`internal/git` (DONE)** — `Repo.Status() (SyncStatus, error)` returns
   `{Branch, HasUpstream, Ahead, Behind, Dirty}` using local refs only (no network); and
   `Repo.Fetch() error` refreshes remote-tracking refs (network, non-fatal on failure).
   Unit-tested in `internal/git/status_test.go`.
2. **daemon** — per-project background poller: periodically `Fetch()` then recompute
   `Status()`, caching `GitStatus` + `lastFetch` timestamp + `fetchError`. Interval
   configurable (default a few minutes); failures non-fatal (offline/auth → keep last
   good status, mark stale). `Server.ListProjects` fills `ProjectInfo.GitStatus` from cache.
3. **proto** — add `GitStatus` message embedded in `ProjectInfo`:
   `branch, has_upstream, ahead, behind, dirty, last_fetch (ts), fetch_error`. Regen Go +
   Swift (Go regen local; Swift regen needs remote BSR plugins per memory).
4. **TUI home** — compact badge in the project list, e.g. `↓3 ↑1 ●` (behind 3 / ahead 1 /
   dirty). Dim/omit when no upstream. Show a subtle staleness/error hint.
5. **iOS** — same badge in the project list.

## Acceptance criteria
- Registering a project with an upstream shows accurate ahead/behind/dirty in TUI + iOS.
- `ListProjects` never blocks on the network (verified: poller does the fetching).
- No upstream / offline / auth failure degrades gracefully (no error surfaced as a crash;
  status shown as unknown/stale).
- Spec §3.1 (projects) updated to document the sync-status field and the cached-fetch model.

## Work log
- 2026-08-06 renumbered 0218 → 0275 (duplicate id detected, 0218 kept by another task)
