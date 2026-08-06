---
id: "0250"
title: 'Worktree bootstrap: [worktree] copy / link / setup / env applied on spawn'
status: todo
priority: 2
created: "2026-08-06"
updated: "2026-08-06"
depends_on: []
spec_refs:
    - docs/design/workstream-integration.md#6. Worktree ergonomics
---

## Description

A fresh linked worktree contains only tracked files: no `.env`, no installed dependencies, no
local config, no build cache. Real projects therefore fail at the first build inside a
workstream, which is the fastest way to abandon worktrees entirely.

Add a per-project `[worktree]` config applied by `SpawnWorkstream` after `git worktree add`
and **before** the session starts:

```toml
[worktree]
copy  = [".env", ".env.local"]    # untracked files seeded from the primary tree
link  = ["node_modules", ".venv"] # symlinked from the primary tree
setup = ["go mod download"]       # commands run once in the new worktree
env   = { }                       # extra env for sessions in this worktree
```

Details: `copy`/`link` entries are workspace-relative and must not escape the tree; missing
sources are skipped silently (a project may not have `.env`). `setup` runs sequentially with
a timeout; a failure aborts the spawn and tears the worktree down (same cleanup path as a
failed session start), with the command output in the error so the user sees why.

## Acceptance criteria

- [ ] `copy`, `link`, `setup`, `env` are parsed and applied on spawn, in that order.
- [ ] Path escapes (`../`, absolute) are rejected; missing sources are skipped.
- [ ] A failing `setup` command aborts the spawn, removes the worktree/branch, and surfaces
      the command's output.
- [ ] Spawning with no `[worktree]` block behaves exactly as today.
- [ ] Tests cover copy/link/setup success and setup failure rollback.


## Work log
