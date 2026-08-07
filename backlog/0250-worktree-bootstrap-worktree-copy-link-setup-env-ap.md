---
id: "0250"
title: 'Worktree bootstrap: [worktree] copy / link / setup / env applied on spawn'
status: done
priority: 2
created: "2026-08-06"
updated: "2026-08-07"
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

## Plan

Implement per-project `[worktree]` bootstrap (copy / link / setup / env) applied by SpawnWorkstream after `git worktree add` and before the session starts (design doc workstream-integration.md §6).

**1. Config surface (internal/config):**
- New `Worktree` struct: `Copy []string`, `Link []string`, `Setup []string`, `Env map[string]string`, `SetupTimeoutSeconds int` (0 => default 300s per command). Add `Worktree Worktree `toml:"worktree,omitempty"`` to `Config`.
- validate(): `SetupTimeoutSeconds >= 0`. Path-escape validation happens at apply time (the lenient loader below bypasses full validate).
- Per-project resolution: a project may commit a `ycc.toml` in its primary tree that carries `[worktree]`. Add a lenient loader `LoadWorktree(dir string) (Worktree, bool)` that reads `dir/ycc.toml` and decodes ONLY the worktree table (decode into a wrapper struct; go-toml ignores unknown keys); returns ok=false on missing file, parse error, or empty block.
- `Registry.WorktreeConfig() Worktree` accessor (under lock, returns a deep copy) as the fallback when the project tree has no `[worktree]` (covers the common single-project case where the discovered daemon config IS the workspace ycc.toml).
- Precedence: project-tree `ycc.toml` `[worktree]` block wins wholly (no field merging); else registry config's `[worktree]`; else zero value (no-op, exactly today's behaviour).

**2. Bootstrap engine (new file internal/workstream/bootstrap.go):**
`func Bootstrap(primary, dir string, cfg config.Worktree) error`, applied in order copy → link → setup:
- Path validation for copy/link entries: entries are workspace-relative; reject anything not `filepath.IsLocal` (absolute, `../`, empty) with a clear error naming the entry. Validation failure is an error (aborts spawn).
- copy: for each entry, source `primary/<p>`; missing source => skip silently. Copy regular files and directories recursively (regular files only inside; preserve file modes; create parent dirs). If the destination already exists in the worktree, skip (never clobber tracked content).
- link: symlink `dir/<p>` -> `primary/<p>` (absolute target); missing source => skip; existing destination => skip; create parent dirs.
- setup: run each command sequentially via `sh -c`, `cmd.Dir = dir`, env = `os.Environ()` + sorted `KEY=VALUE` pairs from cfg.Env, per-command timeout (SetupTimeoutSeconds or 300s default), own process group + kill-group cancel + WaitDelay (mirror tools/worker.go bash discipline). On non-zero exit or timeout: return an error that includes the command and its combined output (tail-truncated ~8KB) so the user sees why.

**3. Spawn integration (internal/session/session.go SpawnWorkstream):**
- After `repo.AddWorktree` succeeds and `cleanup` is defined, resolve the worktree config for `primary` (helper `m.worktreeConfigFor(primary)`: config.LoadWorktree(primary) else m.reg.WorktreeConfig()) and run `workstream.Bootstrap(primary, dir, cfg)`. On error: `cleanup()` (removes worktree, deletes branch, prunes — same path as failed session start) and return `fmt.Errorf("bootstrap worktree: %w", err)`.

**4. env applied to the workstream session:**
- `tools.Workspace` gains `Env []string` (extra KEY=VALUE entries). In internal/tools/worker.go, when non-empty set `cmd.Env = append(os.Environ(), ws.Env...)` for foreground bash (both sandboxed and plain — sandbox.Command returns a plain *exec.Cmd so setting Env works) and for background bash in startBackgroundBash.
- `orchestrator.Deps` gains `Env []string`; BuildMode's coordinator Workspace, the worker Workspace (orchestrator.go ~:467), and the reviewer Workspace (~:743) all carry it so every agent in a worktree session sees the env.
- internal/session/session.go newSession: derive env once — if `m.primaryTreeFor(absWS) != absWS` (i.e. this is a workstream worktree session), load the worktree config for that primary and set `deps.Env` to sorted KEY=VALUE pairs. This covers both fresh spawns and reopened workstream sessions after a daemon restart; primary-tree sessions get no injected env.

**5. Tests:**
- config: `[worktree]` parses (all four keys + timeout), Save/Load round-trip preserves it, LoadWorktree lenient loader works on a worktree-only partial ycc.toml and returns ok=false for missing/absent block.
- workstream/bootstrap_test.go: copy success (content+mode), missing source skipped, existing dest skipped, directory copy; link creates symlink to primary; `../` and absolute entries rejected; setup success (command output side effect visible in worktree); setup failure returns error containing the command output; env visible to setup commands.
- session/workstream_test.go: spawn with a project-tree ycc.toml containing `[worktree]` (copy applied in the new worktree; a failing setup aborts the spawn — SpawnWorkstream errors with the output, worktree dir removed, branch deleted); spawn with no block unchanged (existing tests must keep passing).
- tools/worker_test.go: bash tool respects Workspace.Env (foreground; `echo $FOO`).
- `go build ./... && go test ./...` (known flaky tests per memory: internal/session, internal/setup, internal/tools background-bash — verify against HEAD before blaming new work).

**6. Docs:** short update to docs/design/workstream-integration.md §6 recording the shipped shape (config location/precedence, skip semantics, default timeout, failure behaviour).

### Starting points
- internal/session/session.go:1591-1770 SpawnWorkstream (cleanup closure at ~1713 is the teardown to reuse)
- internal/session/session.go:1442-1476 primaryTreeFor (worktree→primary resolution incl. pre-registration fallback)
- internal/session/session.go:1899-1913 newSession Deps construction
- internal/config/config.go:454-502 Config struct; validate() at :661; Registry at :755
- internal/workstream/paths.go, registry.go — package the new bootstrap.go joins
- internal/tools/worker.go:416-461 foreground bash exec; :480-509 startBackgroundBash
- internal/orchestrator/modes.go:73 coordinator Workspace; orchestrator.go:467 worker Workspace; :743 reviewer Workspace
- internal/session/workstream_test.go newWorkstreamManager helper for spawn tests
- go-toml v2 Unmarshal ignores unknown keys — lenient [worktree]-only decode of a project ycc.toml is safe
- filepath.IsLocal rejects absolute, empty, and ..-escaping paths

## Work log
- 2026-08-07 plan: Implement per-project `[worktree]` bootstrap (copy / link / setup / env) applied by SpawnWorkstream after `git worktree add` and before the session starts (design doc workstream-integration.md §6).  
…[truncated]
- 2026-08-07 context hints: 10 recorded with plan
- 2026-08-07 context hints: SpawnWorkstream + cleanup closure: internal/session/session.go:1591-1770; primaryTreeFor (worktree→primary, pre-registration fallback): internal/session/session.go:1442-1476; newSession Deps constru
…[truncated]
- 2026-08-07 preload: 4 file(s), ~22 KiB seeded into implementer context
- 2026-08-07 implementer report: Implemented task 0250 end to end.  Changes: - Added `[worktree]` config (`copy`, `link`, `setup`, `env`, `setup_timeout_seconds`), validation, lenient per-project `ycc.toml` loading, and a deep-copy r
…[truncated]
- 2026-08-07 review tier: single-opus — reviewers: sol
- 2026-08-07 review (sol): accept — The implementation satisfies task 0250 end to end. It adds and validates the worktree configuration surface, resolves project-level configuration with the documented fallback, applies copy/link/setup 
…[truncated]
- 2026-08-07 decision: accept — commit: Worktree bootstrap: [worktree] copy/link/setup/env applied on spawn (task 0250)  SpawnWorkstream now seeds a fresh linked worktree before its session starts: copy untracked files from the primary tree
…[truncated]
