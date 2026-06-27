---
id: "0031"
title: Backlog browser — view and inspect tasks in the TUI (ListBacklog/GetTask RPCs)
status: todo
priority: 3
created: "2026-06-26"
updated: "2026-06-26"
depends_on:
    - "0006"
spec_refs:
    - 18. Client UI (TUI)
    - 6. Backlog format
---

## Description
## Description

Add an in-app UI to browse and inspect the backlog directly from the TUI, independent of
any agent session. Today the backlog (`internal/docs`, `Store.List`/`Get`) is durable
project state but is only visible indirectly through an agent; clients have no RPC to read
it. See spec §18.5.

### Scope
1. **RPCs** (proto `proto/ycc/v1/ycc.proto` + regenerate): add read-only
   `ListBacklog` (summary rows: id, status, priority, title, depends_on, ready/blocked) and
   `GetTask` (full task: description, acceptance criteria, dependencies, work log). Implement
   on `internal/server` backed by `docs.Store` (and the existing readiness logic —
   `StatusByID` / `BlockingDeps`).
2. **TUI** (`internal/tui`): a modal **backlog browser** view (opened from the home menu
   and/or a key, like the settings overlay) that lists tasks and lets you drill into one to
   read its full detail. Read-only rendering; reuse the overlay/modal patterns already in
   `tui.go`.

### Out of scope
- Mutating the backlog from the UI (quick-add / status changes) — that's task 0016.
- Filtering/sorting is a nice-to-have, not required for a first cut.

## Acceptance criteria
- Daemon exposes `ListBacklog` and `GetTask` RPCs returning backlog data; covered by a
  server test.
- TUI can open a backlog browser, show the task list (id, status, priority, title,
  ready/blocked), and inspect a selected task's full detail (description, acceptance
  criteria, deps, work log), all read-only.
- The browser is reachable from the home menu (and closes back to where you were).
- `go build ./...` and `go test ./...` pass; spec §18.5 stays accurate.

## Work log


## Acceptance criteria

## Work log
