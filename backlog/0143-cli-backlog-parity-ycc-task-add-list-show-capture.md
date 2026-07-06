---
id: "0143"
title: 'CLI backlog parity: ycc task add / list / show (capture from anywhere)'
status: done
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

## Plan

Goal: `ycc task add|list|show` CLI with daemon-backed and daemon-free (direct docs.Store) operation.

Note: the task description assumes a `CreateTask` RPC exists; it does NOT (only ListBacklog/GetTask/UpdateTask/CaptureBacklogItem). Part of this task is adding it.

1. Proto (proto/ycc/v1/ycc.proto): add `rpc CreateTask(CreateTaskRequest) returns (CreateTaskResponse)` next to the other backlog RPCs. `CreateTaskRequest { string project; string title; string body; int32 priority (0 => default 3); repeated string depends_on; repeated string spec_refs; }`, `CreateTaskResponse { TaskDetail task; }`. Regenerate with `buf generate` (buf, protoc-gen-go, protoc-gen-connect-go are all on PATH at /home/why/go/bin).

2. Shared body composer: `taskBody`/`startsWithDescriptionHeader`/`hasHeaderLine` live unexported in internal/orchestrator/modes.go (~line 152). Move them into internal/docs as exported `docs.TaskBody(desc string) string` (plus unexported helpers) and make orchestrator call docs.TaskBody, so server and CLI compose the same "## Description / ## Acceptance criteria / ## Work log" scaffold. Keep behavior identical (existing orchestrator tests must pass).

3. Server (internal/server/server.go): implement `CreateTask` following the UpdateTask pattern: `s.mgr.Backlog(req.Msg.Project)`; validate title non-blank (InvalidArgument), priority 0 (=>3) or 1..5; body := docs.TaskBody(req body); `store.Create(title, body, prio, dependsOn, specRefs)`; return TaskDetail with Ready/BlockedBy computed via docs.StatusByID + docs.BlockingDeps like GetTask does. Add a test in internal/server/server_test.go (there is an existing ListBacklog test at line ~154 to crib the setup from).

4. CLI (new file cmd/ycc/task.go), registered in newRootCommand's Commands list in cmd/ycc/main.go:
   - `ycc task` command group with subcommands add / list / show.
   - Daemon resolution for these commands (do NOT use a.dial(), which would spin up a one-shot in-process daemon): if `a.addr != ""` → daemon.DialClient(a.addr, a.token); else if daemon.Reachable(daemon.LocalAddr, "") → DialClient(local); else direct `docs.NewStore(a.workspace)`. `--project NAME` flag on each subcommand is passed through to RPCs; if set while in direct-store mode, return an error explaining a daemon is required for --project.
   - `ycc task add "title" [--priority N] [--desc TEXT|-] [--depends 0007,0008] [--spec-ref R ...]`: `--desc -` reads the whole description from stdin; comma-separated --depends. Prints the assigned id (e.g. `created 0150`). Direct path: docs.TaskBody + store.Create; daemon path: CreateTask RPC.
   - `ycc task list [--all]`: mirror the coordinator's list_backlog rendering (internal/orchestrator/orchestrator.go ~line 211-260): `ID [status] pN  Title  (deps: a,b)  [READY]|[blocked by ...]` per row, readiness marks only for todo/blocked, done hidden unless --all with a hidden-count note, trailing "Ready to start:" summary. Implement rendering over a small neutral row struct so both the RPC (BacklogTaskSummary) and direct (docs.Task + StatusByID/BlockingDeps) paths share it.
   - `ycc task show <id>`: print frontmatter fields (id, title, status, priority, deps, spec refs, created/updated, readiness) then the markdown body. Shared renderer over a neutral detail struct fed from either TaskDetail or docs.Task.
   - Unit tests (cmd/ycc/task_test.go) for the direct-store path: add creates a file with scaffolded body and prints the id; list output shows readiness marks and hides done; show prints the body. Follow the style of speccheck_test.go (testable core funcs taking io.Writer).

5. Docs: add a `### ycc task` section to docs/cli.md Commands (add/list/show, flags, daemon-free fallback note).

6. Verify: `go build ./... && go vet ./... && go test ./...`; buf generate output committed.

### Starting points
- cmd/ycc/main.go: newRootCommand Commands list (~line 128), resolveDaemon (~598) — do not use a.dial() for task cmds
- cmd/ycc/speccheck.go: pattern for daemon-free command + testable core
- internal/server/server.go:516-616 ListBacklog/GetTask/UpdateTask handlers to crib from
- internal/docs/docs.go: Store.Create/CreateWithStatus (~177), StatusByID (~132), BlockingDeps (~144)
- internal/orchestrator/modes.go:152 taskBody + helpers to move into docs
- internal/orchestrator/orchestrator.go:211-260 list_backlog rendering to mirror
- proto/ycc/v1/ycc.proto: backlog messages ~312-345, rpcs ~559-563; regen: buf generate
- internal/daemon: DialClient, Reachable, LocalAddr
- docs/cli.md: Commands section to extend

## Work log
- 2026-07-06 plan: Goal: `ycc task add|list|show` CLI with daemon-backed and daemon-free (direct docs.Store) operation.  Note: the task description assumes a `CreateTask` RPC exists; it does NOT (only ListBacklog/GetTas
…[truncated]
- 2026-07-06 context hints: 9 recorded with plan
- 2026-07-06 context hints: cmd/ycc/main.go: newRootCommand Commands list (~line 128), resolveDaemon (~598) — do not use a.dial() for task cmds; cmd/ycc/speccheck.go: pattern for daemon-free command + testable core with io.Wri
…[truncated]
- 2026-07-06 implementer report: Implemented `ycc task add|list|show` — CLI backlog capture/browse that works both against a daemon and daemon-free (direct docs.Store).  Changes: 1. **Proto** (proto/ycc/v1/ycc.proto): added `rpc Cr
…[truncated]
- 2026-07-06 review tier: single-opus — reviewers: Claude
- 2026-07-06 review (Claude): accept — The change cleanly implements `ycc task add|list|show` with both daemon-backed and daemon-free (direct docs.Store) operation, exactly per the plan and acceptance criteria. It adds the `CreateTask` RPC
…[truncated]
- 2026-07-06 decision: accept — commit: cli: add `ycc task add|list|show` backlog commands with CreateTask RPC and daemon-free fallback (task 0143)
