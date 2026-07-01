---
id: "0099"
title: 'TUI backlog grooming: edit/reprioritize/status + open task in $EDITOR'
status: todo
priority: 3
created: "2026-07-01"
updated: "2026-07-01"
depends_on: []
spec_refs: []
---

## Description
## Description

The backlog is the project's spine (§6) but the TUI backlog browser (§18.5) is **read-only** — the only mutation path today is the quick-add capture overlay (task 0016). Re-prioritizing, editing acceptance criteria, changing status (todo/blocked/in_review/done), and adjusting dependencies all require going through an agent conversation, which is heavy for routine grooming. For a docs-driven tool, fast direct grooming matters.

Add **direct backlog mutation** from the browser plus an **"open in editor"** escape hatch:

1. **In-TUI edits** for the common cases via the existing daemon RPCs (`UpdateTask`, `CreateTask` — the docs store already serializes writes and regenerates `backlog.md`):
   - change status, change priority, edit title.
   - (nice-to-have) edit acceptance criteria / dependencies.
   The client backlog browser (`internal/tui/tui.go`, `fetchBacklog`/`fetchTask`, `ListBacklog`/`GetTask`) currently only reads; wire the write RPCs, or add thin `UpdateTask`/`CreateTask` client RPCs if not already exposed to the client.

2. **Open in $EDITOR** — a key on a selected task opens its markdown file
   (`backlog/NNNN-*.md`) in the user's `$EDITOR`/`$VISUAL`, then re-reads it on
   return so the browser reflects hand-edits. This is the power-user path for
   anything the structured editor doesn't cover (prose, work log, frontmatter).
   Caveats to design for: this only works when the client shares a filesystem
   with the workspace (the local TUI over the in-process/loopback daemon — the
   common case) — a remote client can't shell out to the workspace's editor, so
   the affordance should be gated/hidden when the workspace isn't local. Suspend
   the Bubble Tea program while the editor runs (`tea.ExecProcess`) and reload
   the task (and regenerate `backlog.md`) afterward.

## Acceptance criteria
- From the backlog browser the user can change a task's status and priority and see it reflected immediately (persisted via the daemon; `backlog.md` regenerated).
- A key opens the selected task's `.md` file in `$EDITOR` (falling back to `$VISUAL`, then a sensible default), suspends the TUI, and reloads on return.
- The open-in-editor affordance is only offered when the workspace is local to the client; it degrades gracefully (hidden/disabled) otherwise.
- No corruption when a work session is concurrently touching the backlog (docs store already serializes writes; validate).
- Build + tests green.

## Acceptance criteria

## Work log
