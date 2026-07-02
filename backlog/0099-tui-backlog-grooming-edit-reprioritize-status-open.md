---
id: "0099"
title: 'TUI backlog grooming: edit/reprioritize/status + open task in $EDITOR'
status: done
priority: 3
created: "2026-07-01"
updated: "2026-07-02"
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

## Plan

Goal: direct backlog grooming from the TUI backlog browser — change status/priority in-TUI (persisted via daemon, backlog.md regenerated) plus an "open in $EDITOR" escape hatch gated to local workspaces.

1) Proto (proto/ycc/v1/ycc.proto):
   - Add `rpc UpdateTask(UpdateTaskRequest) returns (UpdateTaskResponse)` to SessionService.
   - `UpdateTaskRequest { string project = 1; string id = 2; optional string status = 3; optional int32 priority = 4; optional string title = 5; }` — unset fields are untouched. A request with NO mutation fields set is a valid "refresh": re-read the task and regenerate backlog.md (used after hand-edits in $EDITOR).
   - `UpdateTaskResponse { TaskDetail task = 1; }`.
   - Add `string path = 12;` to TaskDetail (absolute task file path) so a local client can gate/drive the open-in-editor affordance.
   - Regenerate with `buf generate` (buf + protoc-gen-go + protoc-gen-connect-go are installed).

2) Server (internal/server/server.go):
   - `UpdateTask`: resolve store via s.mgr.Backlog(project); validate status against docs statuses (todo/in_progress/in_review/done/blocked) and priority 1..5; reject empty title if the title field is set; apply via store.Update(id, mut) (bumps Updated), then store.RenderIndex() (Update alone does NOT regenerate backlog.md); recompute ready/blocked_by and return TaskDetail incl. Path.
   - Set `Path` in GetTask's TaskDetail too (t.Path is already populated by parseFile).

3) TUI (internal/tui/tui.go), backlog browser (updateBacklog / backlogView / taskDetailView):
   - List view (cursor on a row) and detail view both support:
     - `+`/`=` raise priority (toward p1), `-` lower (toward p5) — clamp 1..5; fire an UpdateTask cmd; on response refresh list (fetchBacklog) and detail if open.
     - `s` opens a tiny inline status prompt (footer/hint line): digits 1..5 map to todo/in_progress/in_review/done/blocked, esc cancels. Selecting fires UpdateTask{status}.
   - New msg types (e.g. taskUpdatedMsg{task}/errMsg) handled in Update: update m.backlogDetail if it's the same id, and re-fetch the backlog list; surface errors via a transient status/hint line rather than crashing.
   - Open in $EDITOR: key `e` in the detail view (and list view if trivial). Only offered when the task file is local: gate on `os.Stat(detail.Path) == nil` (client shares the workspace fs — the common in-process/loopback case); hide the `e` hint and no-op (with a brief "workspace not local" notice) otherwise. Resolve editor as $EDITOR, then $VISUAL, then "vi". Use tea.ExecProcess(exec.Command(editor, path), func(err) tea.Msg) to suspend the TUI; on return, send the no-mutation UpdateTask refresh (regenerates backlog.md + bumps Updated), then fetchTask(id) + fetchBacklog so hand-edits show up.
   - Update the hint lines: list "… + / - priority · s status · e edit · …" (e only when local), detail likewise.
   - Note: in-TUI title editing is out of scope (the RPC supports it for the future; $EDITOR covers prose/title edits).

4) Tests:
   - internal/server/server_test.go: extend TestBacklogRPCs (or add TestUpdateTask): status + priority changes persist and backlog.md is regenerated; invalid status/priority rejected; no-field refresh regenerates the index; Path populated in GetTask.
   - internal/tui/tui_test.go: keypress-level tests following existing backlog tests — `+`/`-`/`s`+digit produce UpdateTask calls (via the test client) and the model refreshes; status-prompt esc cancels; editor affordance hidden when the path does not exist locally; editor command resolution ($EDITOR → $VISUAL → vi) unit-tested via a helper func.
   - `go build ./... && go test ./...` green; `go vet ./...` clean.

Concurrency: the docs store already serializes writes per backlog dir (lockFor); server-side Update+RenderIndex under that store is the same path the capture agent and orchestrator use — no new locking needed.

### Starting points
- proto/ycc/v1/ycc.proto — backlog messages at ~L294-324, service at ~L442; regen with `buf generate`
- internal/server/server.go — ListBacklog/GetTask handlers at L485-526; s.mgr.Backlog(project) resolves the docs store
- internal/docs/docs.go — Store.Update(id, mut) bumps Updated but does NOT regenerate backlog.md; call Store.RenderIndex() after; Task.Path is set by parseFile
- internal/tui/tui.go — updateBacklog (L3053), backlogView (L3126), taskDetailView/refreshBacklogDetailVP (~L3188), fetchTask/fetchBacklog (~L1237); msgs like backlogMsg/taskMsg around L406
- bubbletea v2: tea.ExecProcess(cmd, func(error) tea.Msg) suspends the program while an external process runs
- tests: internal/server/server_test.go TestBacklogRPCs (L135); internal/tui/tui_test.go TestBacklogHidesDoneByDefault (L1608) shows the fake-client + keypress test pattern

## Work log
- 2026-07-02 plan: Goal: direct backlog grooming from the TUI backlog browser — change status/priority in-TUI (persisted via daemon, backlog.md regenerated) plus an "open in $EDITOR" escape hatch gated to local worksp
…[truncated]
- 2026-07-02 context hints: 6 recorded with plan
- 2026-07-02 context hints: proto/ycc/v1/ycc.proto — backlog messages at ~L294-324, service rpcs at ~L442; regen with `buf generate` (buf, protoc-gen-go, protoc-gen-connect-go all on PATH); internal/server/server.go — ListBa
…[truncated]
- 2026-07-02 implementer report: Implemented Task 0099: direct backlog grooming from the TUI backlog browser plus an "open in $EDITOR" escape hatch.  ## Proto (proto/ycc/v1/ycc.proto) - Added `UpdateTask(UpdateTaskRequest) returns (U
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The change correctly implements TUI backlog grooming for task 0099. Proto adds UpdateTask RPC (with optional status/priority/title mutation fields and a no-field refresh semantics) plus a Path field o
…[truncated]
- 2026-07-02 decision: accept — commit: tui: backlog grooming — status/priority edits via new UpdateTask RPC + open task in $EDITOR (0099)
- 2026-07-02 usage: 40,528 tok (in 184, out 40,344, cache_r 3,690,942, cache_w 159,375) · cost n/a (unpriced)
  implementer: 30,332 tok (in 132, out 30,200, cache_r 2,867,907, cache_w 68,007) · cost n/a (unpriced)
  coordinator: 5,932 tok (in 22, out 5,910, cache_r 567,039, cache_w 64,720) · cost n/a (unpriced)
  reviewer:Claude: 4,264 tok (in 30, out 4,234, cache_r 255,996, cache_w 26,648) · cost n/a (unpriced)
