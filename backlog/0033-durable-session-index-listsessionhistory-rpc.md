---
id: "0033"
title: Durable session index + ListSessionHistory RPC
status: done
priority: 3
created: "2026-06-27"
updated: "2026-06-28"
depends_on: []
spec_refs: []
---

## Description
Make persisted sessions discoverable. Today `ListSessions` only returns live, in-memory
sessions; on-disk logs at `<workspace>/.ycc/sessions/<id>/events.jsonl` are orphaned after
a daemon restart or GC. This task adds read-only enumeration of all sessions for a project
(live + persisted) by scanning and reducing the logs. Foundational for the session browser
(reopen UI) and reused by the cost view.

## Context
- Source of truth already exists: `internal/event/log.go` (append-only JSONL) and
  `internal/event/reduce.go` (`Reduce` → Projection: Mode, Status, FocusTask, TurnsByTask).
- `internal/session/session.go` `Manager.ListByProject` returns only live sessions;
  `internal/server/server.go` `ListSessions` wraps it into `SessionInfo`.
- Spec §18.6 (new), §5.1 storage, §20.3 aggregation.

## Acceptance criteria
- [ ] A session-index helper (in `internal/session` or a small new package) scans a
      workspace's `.ycc/sessions/*/events.jsonl`, reduces each, and returns summary rows:
      id, mode, status, started-at + last-activity timestamps, focused task(s), a short
      title derived from the first user prompt/kickoff, and turn counts. Live sessions in
      the manager map override their on-disk snapshot (live status wins).
- [ ] `ListSessionHistory(project)` RPC added to `SessionService` returning these summary
      rows (most-recent first). Existing `ListSessions` keeps meaning "live only".
- [ ] Robust to partial/corrupt/empty logs (skip with a logged warning, don't crash).
- [ ] Tests over fixture session dirs: ordering, live-overrides-disk, title derivation,
      and a malformed log is tolerated.

## Acceptance criteria

## Work log
- 2026-06-28 plan: Add durable session-index enumeration (live + persisted) and a ListSessionHistory RPC.  1. internal/session/history.go (new):    - `SessionSummary` struct: ID, Mode, Status (event.Status), Workspace, 
…[truncated]
- 2026-06-28 implementer report: Implemented Task 0033: durable session index + ListSessionHistory RPC.  Changes: - internal/session/history.go (new): `SessionSummary` struct; `scanSessionHistory(workspace)` globs `<ws>/.ycc/sessions
…[truncated]
- 2026-06-28 review tier: single-opus — reviewers: Claude
- 2026-06-28 review (Claude): accept — The change fully satisfies Task 0033. A new `internal/session/history.go` provides `SessionSummary` and `scanSessionHistory`, globbing `<ws>/.ycc/sessions/*/events.jsonl`, reducing each log via `event
…[truncated]
- 2026-06-28 decision: accept — commit 441091d: Add durable session index + ListSessionHistory RPC  Enumerate all sessions for a project (live + persisted on-disk logs) by scanning .ycc/sessions/*/events.jsonl and reducing each. Adds SessionSummary
…[truncated]
- 2026-06-28 usage: 20,215 tok (in 72, out 20,143, cache_r 1,332,256, cache_w 96,183) · cost n/a (unpriced)
