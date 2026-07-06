---
id: "0143"
title: 'CLI backlog parity: ycc task add / list / show (capture from anywhere)'
status: todo
priority: 3
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 6.2 Backlog — structured items, markdown-rendered
    - docs/cli.md#Commands
---

## Description
The TUI has quick-add capture (ctrl+n) and a backlog browser, but the shell has nothing: you can't jot a task from outside the TUI, from a git hook, or from another tool. Backlog read/write RPCs already exist (`ListBacklog`/`GetTask`/`CreateTask`…); this is CLI plumbing plus the same daemon-free fallback `spec-check` uses (`docs.Store` directly when no daemon is running).

## Acceptance criteria
- [ ] `ycc task add "title" [--priority N] [--desc -|TEXT] [--depends 0007,0008]` creates a task (id printed), reading a long description from stdin with `--desc -`.
- [ ] `ycc task list` mirrors `list_backlog` (id, status, priority, title, readiness); `ycc task show 0007` prints the full task.
- [ ] Works without a running daemon (direct `docs.Store` on the workspace), and against `--addr`/`--project` when a daemon is available.

## Work log
