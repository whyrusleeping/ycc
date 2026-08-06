---
id: "0255"
title: 'ycc ws list / ycc ws path: reach a workstream worktree from the shell'
status: todo
priority: 3
created: "2026-08-06"
updated: "2026-08-06"
depends_on: []
spec_refs:
    - docs/design/workstream-integration.md#7. Surfaces
    - docs/cli.md
---

## Description

Worktrees live under `<state>/ycc/worktrees/<project>/<id>` (deliberately — see
`docs/design/workstream-integration.md` §2), which is tidy but not a path anyone types twice.
Add a small CLI so a human can get into one:

- `ycc ws list [--project P]` — id, task, branch, status, commit count, session id.
- `ycc ws path <id>` — prints the absolute worktree path and nothing else, so
  `cd $(ycc ws path ws_3f9a)` works.
- Accept an unambiguous id prefix (`3f9a` → `ws_3f9a`).

Both go through the existing `ListWorkstreams` RPC so they work against a remote daemon.

## Acceptance criteria

- [ ] `ycc ws path <id>` prints only the path (suitable for command substitution) and exits
      non-zero with a message on unknown/ambiguous ids.
- [ ] `ycc ws list` renders the same data as the TUI panel, respecting `--project`.
- [ ] Both work against a remote daemon (`-addr`).
- [ ] Documented in `docs/cli.md`.


## Work log
