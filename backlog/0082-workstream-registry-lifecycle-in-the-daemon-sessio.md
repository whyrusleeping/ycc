---
id: "0082"
title: Workstream registry + lifecycle in the daemon/session manager
status: todo
priority: 4
created: "2026-06-30"
updated: "2026-06-30"
depends_on:
    - "0081"
spec_refs:
    - Daemon lifecycle & projects
    - Session & event log
    - Persistence & remote sync
---

## Description
## Context
Second step of the parallel-workstreams design (see `docs/design/parallel-workstreams.md` §5, §7, §10.2). Introduce a daemon-owned workstream concept and lifecycle on top of the git worktree primitives.

## Scope
- A daemon-owned, serialized **workstream registry** (id → {project, base commit, branch, worktree path, session id, status}), persisted in the daemon state dir beside `projects.json`.
- `Manager.SpawnWorkstream(...)` creates the worktree (via the 0081 primitives) and starts a `work` session scoped to the worktree path.
- Startup recovery: reconcile stale worktrees via `git worktree list`/`prune`.

## Acceptance criteria
- [ ] A workstream registry persists across daemon restart and is serialized like the project registry.
- [ ] A workstream is a CHILD of a project (not a new top-level project entry); the project picker is not polluted with worktrees.
- [ ] `SpawnWorkstream` creates a worktree + branch (`ycc/ws/<id>`) and starts a session scoped to it, with `.ycc/` logs living under the worktree.
- [ ] Single-writer invariant preserved: at most one writing session per worktree path; a second writer for the same path is rejected.
- [ ] Startup reconciles/prunes stale worktrees from a crashed daemon.
- [ ] Tests cover spawn + persistence + recovery; build/vet/test pass.

## Acceptance criteria

## Work log
