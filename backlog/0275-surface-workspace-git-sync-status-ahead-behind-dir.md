---
id: "0275"
title: Surface workspace git sync status (ahead/behind/dirty) in the app
status: in_review
priority: 3
created: "2026-07-16"
updated: "2026-08-09"
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

## Plan

Layer 1 (internal/git: Repo.Status() SyncStatus + Repo.Fetch(), unit tests) is already committed. Remaining layers:

1. **Proto** — add to proto/ycc/v1/ycc.proto:
   ```
   message GitStatus {
     string branch = 1;        // "" when detached
     bool has_upstream = 2;
     int32 ahead = 3;
     int32 behind = 4;
     bool dirty = 5;
     int64 last_fetch_unix = 6; // 0 = poller has not fetched yet
     string fetch_error = 7;    // last fetch failure; "" when ok
   }
   ```
   and `GitStatus git = 3;` on ProjectInfo. Regen per plans/build-and-test.md: `buf generate` (Go, local plugins) and `buf generate --template buf.gen.swift.yaml` (Swift, remote BSR plugins, needs network — if it fails offline, note it in the report).

2. **Daemon poller (internal/session)** — new file e.g. internal/session/gitsync.go:
   - A Manager-owned background goroutine that every interval (default ~3 min, interval settable on Manager for tests) iterates registered projects, and for each project path that is a git repo calls `(&git.Repo{Dir: path}).Fetch()`, caching per-path `{lastFetch time.Time, fetchError string}` under the Manager mutex (or its own small mutex). Non-repos are skipped silently. Fetch failures are non-fatal: record fetchError, keep going.
   - CRITICAL (memory gotcha): Manager goroutines running git must be joinable — wire it to a ctx cancelled and a WaitGroup waited in ReclaimAll (mirror the integrationCtx/integrationWG pattern at session.go ~1385/~2494). Start it in NewManager (do the first fetch cycle after a short initial delay or immediately in the loop — either is fine, but ListProjects must never wait on it).
   - Per the task design, dirty/ahead/behind are always FRESH: expose a Manager method (e.g. `ProjectGitStatus(path string) *v1.GitStatus` or a struct the server converts) that computes `git.Repo.Status()` inline — local refs only, no network — and merges the cached lastFetch/fetchError. Return nil for non-repo paths.

3. **Server** — Server.ListProjects fills `ProjectInfo.Git` from that method. No network on this path (Status() is local-only; only the poller fetches).

4. **TUI** — pickerScreenView (internal/tui/picker.go): compact badge per project row, e.g. `↑1 ↓3 ●` (ahead/behind/dirty; ● only when dirty). Omit ahead/behind when !HasUpstream. When fetch_error is set or last_fetch is 0, add a subtle dim hint (e.g. dim `?` or `stale`). Build the badge in a small pure helper func and unit-test it (table test of GitStatus → string). Keep the row layout readable at 80 cols.

5. **iOS** — clients/ios/App/WorkspaceDrawer.swift project rows (~line 199): show the same compact badge (e.g. secondary-color caption "↑1 ↓3 ●") when project.git is present and has something to say (dirty or ahead/behind or no-upstream can be shown dimmed/omitted — keep it quiet when in sync). Prefer putting the badge-string formatting in YccKit (e.g. a small helper in an appropriate Sources/YccKit file) so it's testable with `swift test` later; the view just renders the string. Note: no Swift toolchain in this environment — do NOT try to build Swift; keep the change minimal and obviously correct.

6. **Spec** — spec.md §3.1: document the GitStatus field on ProjectInfo and the cached-fetch model (background poller fetches periodically; ListProjects computes local status inline and never touches the network; failures degrade to stale + fetch_error). Also update the RPC surface note near line ~960 where ProjectInfo fields are listed.

7. **Tests** — 
   - internal/session: a test that builds a temp origin repo + clone (see internal/git/status_test.go for helpers/patterns), registers it as a project, and asserts the Manager status method reports ahead/behind/dirty correctly; and that a fetch failure (e.g. remote removed) records fetchError without breaking anything. Keep goroutines joined (TempDir race gotcha).
   - internal/server: ListProjects returns populated Git for a git-repo project and nil/empty for a non-repo dir.
   - internal/tui: badge helper table test.

8. **Verify** — `go build ./... && go vet ./...` and `go test ./internal/git/... ./internal/session/... ./internal/server/... ./internal/tui/...` (full `go test ./...` has known flakes; verify targeted packages).

NOTE: the worktree contains OTHER tasks' uncommitted work (proto, server.go, session.go, spec.md, WorkspaceDrawer.swift are already modified). Do not revert, reformat, or "clean up" anything unrelated — touch only what this task needs, keep edits surgical. The coordinator will do a selective commit.

### Starting points
- internal/git/git.go: SyncStatus struct + Repo.Status()/Repo.Fetch() (~lines 98-148) — already committed layer 1
- internal/session/session.go: Manager struct ~1375 (integrationCtx/integrationWG pattern), NewManager ~1420, Projects() ~1567, ReclaimAll ~2494 (join point)
- internal/server/server.go:88 ListProjects
- proto/ycc/v1/ycc.proto:47 ProjectInfo; regen commands in plans/build-and-test.md
- internal/tui/picker.go:72 pickerScreenView (project rows), internal/tui/msgs.go projectsMsg
- clients/ios/App/WorkspaceDrawer.swift ~199 (ForEach model.projects → DrawerRow)
- internal/git/status_test.go: temp-repo test helpers to reuse
- memory gotcha: Manager goroutines running git after events must be joinable (Manager teardown waits) or TempDir tests race

## Work log
- 2026-08-06 renumbered 0218 → 0275 (duplicate id detected, 0218 kept by another task)
- 2026-08-09 plan: Layer 1 (internal/git: Repo.Status() SyncStatus + Repo.Fetch(), unit tests) is already committed. Remaining layers:  1. **Proto** — add to proto/ycc/v1/ycc.proto:    ```    message GitStatus {      
…[truncated]
- 2026-08-09 context hints: 8 recorded with plan
- 2026-08-09 context hints: internal/git/git.go: SyncStatus struct + Repo.Status()/Repo.Fetch() (~lines 98-148) — already committed layer 1; internal/session/session.go: Manager struct ~1375 (integrationCtx/integrationWG patte
…[truncated]
- 2026-08-09 preload: 5 file(s), ~14 KiB seeded into implementer context
- 2026-08-09 implementer report: Implemented task 0275 across proto, daemon, server, TUI, iOS, spec, and tests.  Changes: - Added `GitStatus` and `ProjectInfo.git` to the proto; regenerated Go and Swift protobuf outputs successfully 
…[truncated]
- 2026-08-09 review tier: single-opus — reviewers: sol
- 2026-08-09 review (sol): revise — The proto, daemon-side cached fetch loop, local-only status merge, server projection, badges, spec updates, and targeted tests are otherwise well integrated, and the requested Go build/vet/tests pass.
…[truncated]
- 2026-08-09 revision: Addressed the reviewer’s snapshot-refresh finding.  Changes: - TUI project picker now starts a 3-second `projectsTickMsg` loop from `Init` when `statePicker` is active. - Each active-picker tick bat
…[truncated]
- 2026-08-09 review (sol): accept — The revision addresses the stale-snapshot issue: the TUI now re-polls the local-only `ListProjects` status while the picker is active, with a test covering refresh and cancellation after leaving the p
…[truncated]
