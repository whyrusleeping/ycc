---
id: "0082"
title: Workstream registry + lifecycle in the daemon/session manager
status: done
priority: 4
created: "2026-06-30"
updated: "2026-07-02"
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

## Plan

Implement a daemon-owned workstream registry + lifecycle per docs/design/parallel-workstreams.md §5, §7, §10.2, layered on the 0081 git primitives.

1. New package `internal/workstream` (mirror `internal/project/registry.go` style):
   - `Workstream` struct: `ID`, `Project` (parent project NAME — a child of a project, never a project-registry entry), `BaseCommit`, `Branch`, `WorktreePath`, `SessionID`, `TaskID` (optional), `Status`, `CreatedAt`.
   - Status constants: `StatusActive`, `StatusMerged`, `StatusDiscarded`, `StatusStale` (merged/discarded transitions get used by 0083/0084; define now).
   - `Registry`: concurrency-safe, JSON-file-backed like project.Registry — `Open(path)`, `NewMemory()`, `StateFile()` → `<state>/ycc/workstreams.json` (beside projects.json), atomic tmp+rename save, `Add(ws)`, `Get(id)`, `List()`, `ListByProject(name)`, `SetStatus(id, status)` (persisting), `Remove(id)`.
   - Single-writer invariant enforced in `Add`: reject (typed/sentinel error) when another workstream with status active already records the same worktree path.
   - `DefaultWorktreesRoot()` → `<state>/ycc/worktrees` (XDG_STATE_HOME fallback like project.StateFile).

2. `internal/session` Manager changes:
   - Fields `workstreams *workstream.Registry` + `worktreesRoot string`; `SetWorkstreams(reg, root)` (default in-memory registry in NewManager so nil-checks aren’t needed everywhere; root may default to DefaultWorktreesRoot).
   - Refactor `Start` minimally: extract the body into an unexported `start(cfg Config, autoRegisterProject bool)` so the worktree path is NOT auto-registered as a project (AC: picker not polluted). `Start` calls `start(cfg, true)`.
   - `SpawnWorkstreamConfig{Project, BaseRef, TaskID, Prompt, InteractionLevel}` and `Manager.SpawnWorkstream(cfg) (workstream.Workstream, *Session, error)`:
     a. Resolve project name → primary path (error on unknown project; Project required).
     b. `git.Open(primary)`; resolve base commit: `rev-parse` of BaseRef (default HEAD).
     c. New id `ws_<8-hex>` (mirror newID); branch `ycc/ws/<id>` with `-<taskID>` suffix when TaskID set (design §5: e.g. `ycc/ws/ws_3f9a-0042`).
     d. Worktree dir `<worktreesRoot>/<project>/<id>` (out of the primary tree, per design §5).
     e. Enforce single-writer: reject when the registry already has an active workstream for the same worktree path, and also when a live session in m.sessions is already scoped to that path.
     f. `repo.AddWorktree(dir, branch, baseCommit)`; then start a `work`-mode session via `start(..., autoRegister=false)` with Workspace=worktree dir (session's `.ycc/` lands under the worktree automatically). On session-start failure, best-effort cleanup: RemoveWorktree + `git branch -D` (add a small helper or use repo.run-equivalent if needed) + prune, then return the error.
     g. Persist the registry entry (status active, session id filled in); if persisting fails, clean up as above.
   - Accessors: `Workstreams(project string) []workstream.Workstream` (empty project = all) for later RPC use.
   - `Manager.ReconcileWorkstreams() error` — startup recovery: for each project having non-terminal (active) workstreams: open the primary repo (if the project path is gone, mark its workstreams stale), `PruneWorktrees()`, `ListWorktrees()`; any active workstream whose worktree dir is missing or absent from the list → `SetStatus(stale)`. Conservative: never delete unknown worktrees or dirty trees; prune only reaps git's stale admin entries.

3. Daemon wiring (`internal/daemon/serve.go` buildHandler): when `o.Persist`, `workstream.Open(workstream.StateFile())`, `mgr.SetWorkstreams(wreg, workstream.DefaultWorktreesRoot())`, then `mgr.ReconcileWorkstreams()` (log, don't fail startup, on reconcile error). One-shot path keeps the in-memory default.

4. Tests:
   - `internal/workstream/registry_test.go`: add → persisted file → reopen sees it (persistence across restart); duplicate active worktree path rejected; SetStatus persists; ListByProject filters; discarded/merged entry frees the path for a new active one.
   - `internal/session/workstream_test.go` (build a temp git repo with an initial commit via internal/git, use `testRegistry()` manager harness):
     - SpawnWorkstream creates the worktree dir + branch `ycc/ws/<id>...` (verify via git.ListWorktrees / rev-parse), starts a session whose Workspace == worktree path, `.ycc/` log exists under the worktree, registry entry recorded active with the session id; project registry (m.Projects()) does NOT gain a new entry.
     - Second spawn targeting the same path (simulate by pre-adding an active registry entry with that path) is rejected.
     - Recovery: create registry entries, remove a worktree dir out-of-band (`rm -rf`), run ReconcileWorkstreams → that entry becomes stale, surviving one stays active; registry persisted across a fresh Open (restart simulation).
     - Stop spawned sessions at test end to avoid leaking goroutines (backend is a dead localhost, loop will just error — fine).
   - `go build ./... && go vet ./... && go test ./...` pass.

Scope note: merge/discard flows, RPC surface, and TUI are tasks 0083–0085 — do NOT add proto/server/TUI changes here beyond the manager + registry + daemon wiring.

### Starting points
- internal/project/registry.go — pattern to mirror for the workstream registry (Open/NewMemory/StateFile/atomic save)
- internal/session/session.go: Manager.Start (~line 907) auto-registers workspace via EnsureWorkspace — refactor so workstream sessions skip it; newID() at ~1472
- internal/git/worktree.go + merge.go — AddWorktree/RemoveWorktree/ListWorktrees/PruneWorktrees from task 0081
- internal/daemon/serve.go buildHandler — where the persistent project registry is wired (o.Persist)
- internal/session/settings_test.go testRegistry() — manager test harness; project_test.go for style
- docs/design/parallel-workstreams.md §5 (branch ycc/ws/<id>, worktree dir under state dir keyed by project), §7 (registry shape, child-of-project), §10.2

## Work log
- 2026-07-02 plan: Implement a daemon-owned workstream registry + lifecycle per docs/design/parallel-workstreams.md §5, §7, §10.2, layered on the 0081 git primitives.  1. New package `internal/workstream` (mirror `in
…[truncated]
- 2026-07-02 context hints: 6 recorded with plan
- 2026-07-02 context hints: internal/project/registry.go — pattern to mirror for the workstream registry (Open/NewMemory/StateFile/atomic tmp+rename save); internal/session/session.go: Manager.Start ~line 907 (auto-registers w
…[truncated]
- 2026-07-02 implementer report: Implemented the daemon-owned workstream registry + lifecycle (task 0082) on top of the 0081 git worktree primitives.  ## Changes  **New package `internal/workstream` (registry.go)** - `Workstream` str
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The change fully implements task 0082: a daemon-owned, persistent workstream registry (mirroring project.Registry with atomic saves and single-writer enforcement via ErrWorktreeInUse), Manager.SpawnWo
…[truncated]
- 2026-07-02 decision: accept — commit: session: daemon-owned workstream registry + spawn/reconcile lifecycle on git worktrees (0082)
