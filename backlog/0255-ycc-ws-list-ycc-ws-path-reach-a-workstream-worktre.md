---
id: "0255"
title: 'ycc ws list / ycc ws path: reach a workstream worktree from the shell'
status: done
priority: 3
created: "2026-08-06"
updated: "2026-08-08"
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

## Plan

Add a `ycc ws` command group (client-side only, via the existing ListWorkstreams RPC so it works against -addr remote daemons).

1. New file cmd/ycc/ws.go:
   - `(a *app) wsCommand() *cli.Command` with Name "ws", Aliases ["workstream","workstreams"], and subcommands:
     - `list` (flag --project): call ListWorkstreams{Project}, render a tabwriter table with the same data as the TUI Workstreams panel: ID, TASK, BRANCH (trim the "ycc/ws/" prefix like tui.shortBranch), COMMITS (N↑ or plain count), STATUS (status, plus session_status for non-terminal like the TUI's wsRowStatus does), SESSION. Print "(no workstreams)" when empty. `ycc ws` with no subcommand behaves like `list` (mirroring `ycc project`), rejecting unknown positionals.
     - `path <id>`: resolve the id (see below), print ONLY the absolute worktree_path to stdout (fmt.Println, nothing else), so `cd $(ycc ws path ws_3f9a)` works. On unknown/ambiguous id return an error (goes to stderr via fatal(), non-zero exit). Ambiguous error should list the candidate ids.
   - Prefix resolution as a pure, testable function, e.g. `resolveWorkstreamID(workstreams []*v1.WorkstreamInfo, arg string) (*v1.WorkstreamInfo, error)`:
     - exact id match wins;
     - else treat arg as a prefix, matching both `arg` and `ws_`+arg against ids (so `3f9a` → `ws_3f9a`);
     - exactly one match → return it; zero → "unknown workstream %q"; several → ambiguous error naming the matches.
2. cmd/ycc/ws_test.go: unit tests for resolveWorkstreamID (exact, bare-prefix, ws_-prefix, unknown, ambiguous) and ideally the row-formatting helper.
3. cmd/ycc/main.go: register a.wsCommand() in the root Commands list (single-line hunk).
4. docs/cli.md: add a `### ycc ws <list|path> — reach workstream worktrees` section after the project section (or near task), documenting both subcommands, the prefix rule, the command-substitution use (`cd $(ycc ws path 3f9a)`), and that they work against `--addr` remote daemons.

Verify: go build ./... && go vet ./cmd/ycc && go test ./cmd/ycc.

NOTE for commit stage (coordinator): tree holds other tasks' uncommitted work; main.go already carries task 0294's rename hunk — commit selectively (new files fully; main.go via hunk-filtered index build, pre-diff snapshot saved at /tmp/t0255/pre.diff).

### Starting points
- cmd/ycc/main.go: newRootCommand Commands list; projectCommand shows the no-subcommand-lists pattern
- internal/tui/workstreams.go: wsRowStatus + shortBranch — the panel semantics `ws list` should mirror
- proto WorkstreamInfo fields: id, project, branch, worktree_path, session_id, task_id, status, commit_count, session_status, base_branch, created_at
- a.dial() returns a connected SessionServiceClient honoring --addr/--token
- cmd/ycc/task.go: existing table-rendering CLI style to match
- Do NOT touch the rename subcommand already present in main.go (another task's in-flight work)

## Work log
- 2026-08-08 plan: Add a `ycc ws` command group (client-side only, via the existing ListWorkstreams RPC so it works against -addr remote daemons).  1. New file cmd/ycc/ws.go:    - `(a *app) wsCommand() *cli.Command` wit
…[truncated]
- 2026-08-08 context hints: 6 recorded with plan
- 2026-08-08 context hints: cmd/ycc/main.go: newRootCommand Commands list (~line 132); projectCommand shows the no-subcommand-lists pattern; internal/tui/workstreams.go: wsRowStatus + shortBranch — the panel semantics ws list 
…[truncated]
- 2026-08-08 preload: 2 file(s), ~15 KiB seeded into implementer context
- 2026-08-08 implementer report: Implemented task 0255.  Changes: - Added `cmd/ycc/ws.go` with the `ycc ws` command group and `workstream`/`workstreams` aliases. - Added `ws list [--project P]`, backed by `ListWorkstreams`, rendering
…[truncated]
- 2026-08-08 review tier: single-opus — reviewers: sol
- 2026-08-08 review (sol): accept — The new `ycc ws` command group satisfies the task: list and path both use `ListWorkstreams` through the existing daemon-aware dial path, list supports project filtering and mirrors the TUI row semanti
…[truncated]
