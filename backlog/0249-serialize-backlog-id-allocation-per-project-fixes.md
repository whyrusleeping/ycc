---
id: "0249"
title: Serialize backlog id allocation per project (fixes duplicate ids from parallel worktrees)
status: done
priority: 1
created: "2026-08-06"
updated: "2026-08-07"
depends_on: []
spec_refs:
    - docs/design/workstream-integration.md#6. Worktree ergonomics
---

## Description

`docs.Store.nextID` (`internal/docs/docs.go:332`) allocates ids as `max(existing)+1` by
scanning the backlog directory **of the current tree**. Each workstream worktree has its own
checkout of `backlog/`, so two parallel streams that both create a task mint the *same* id,
and the collision only shows up after merge. This repo already carries 14 duplicate ids
(0120, 0175, 0192–0195, 0211–0218) — see task 0242 — which is almost certainly this bug.

Make id allocation a per-project, daemon-serialized operation: a counter in the daemon state
dir keyed by project, seeded from `max(existing ids in the primary tree)` on first use and
monotonically bumped under a lock. Worktree sessions ask the daemon for the next id rather
than scanning their own tree. Fall back to today's scan when there is no daemon-owned project
(one-shot in a plain directory) — that path has no parallelism.

## Acceptance criteria

- [ ] Two concurrent `create_task` calls from two different worktrees of the same project
      receive different ids (test).
- [ ] The counter seeds correctly from an existing backlog and never re-issues an id already
      present in the primary tree.
- [ ] A tree with no daemon project still allocates ids as before.
- [ ] Task 0242's cleanup is unblocked (duplicates stop being regenerated).

## Plan

Goal: stop parallel worktrees from minting duplicate backlog ids by routing all daemon-side id allocation through one per-project, serialized, persistent counter, while keeping today's scan for daemon-less trees.

Key facts (verified):
- `docs.Store.nextID` (internal/docs/docs.go:354) scans the CURRENT tree only → two worktrees mint the same id.
- All sessions (primary + workstream worktrees) run inside the daemon's `session.Manager`; `ycc task` prefers a daemon RPC when one is reachable, and only falls back to a direct `docs.NewStore` when none is (cmd/ycc/task.go). So a daemon-process-level allocator covers the real parallelism; cross-process reservation is a separate proposed task (0276) — out of scope.
- Store creation sites to wire: `Manager.newSession` (Deps.Docs, session.go:1844), `Manager.Backlog` (session.go:2393, backs CreateTask RPC), `Manager.CaptureBacklogItem` (session.go:2416). `cmd/ycc` direct backend and tui stay on the plain scan fallback.

Design:
1. New file internal/docs/idalloc.go:
   - `maxTaskID(dir string) (int, error)`: lock-free scan of a backlog dir (reuse parseFile/normalizeID) returning the max numeric id; 0 for missing dir. MUST NOT take dirLocks — a primary-tree Store holds its dir lock while allocating, and the allocator scans that same dir (deadlock otherwise).
   - `type IDAllocator` with its own mutex and an optional backing file: persistent counters as JSON `map[string]int` keyed by the project's primary backlog dir (abs path). `AllocatorFor(path string) *IDAllocator` returns a package-level shared instance per path (registry map like lockFor) so every Store in the process shares one; path "" = in-memory only (map kept in the struct), still shared via the registry.
   - `(*IDAllocator).NextID(primaryBacklogDir string) (string, error)`: under the mutex — load counters from file (missing = empty), seed/floor from `maxTaskID(primaryBacklogDir)`, next = max(counter, scanMax)+1, persist atomically (MkdirAll + tmp + rename), return `%04d`. Re-flooring against the primary scan on every call guarantees we never re-issue an id already present in the primary tree even if someone hand-adds files.
   - `DefaultIDStateFile() string` → `<XDG_STATE_HOME|~/.local/state>/ycc/backlog-ids.json` (mirror workstream.stateDir; fall back to os.TempDir like the others).
2. internal/docs/docs.go: Store gains an optional id source — `func (s *Store) SetIDSource(fn func() (string, error))`; `nextID()` uses fn when set, else the existing scan (fallback path unchanged).
3. internal/session/session.go:
   - Manager field `idAlloc *docs.IDAllocator`, default `docs.AllocatorFor("")` in NewManager (in-memory: fixes the live bug even without persistence); `SetIDStateFile(path string)` setter.
   - internal/daemon/serve.go: wire `mgr.SetIDStateFile(docs.DefaultIDStateFile())` next to the SetWorkstreams/SetProjects wiring (persistent daemon only; one-shot/in-memory daemons keep the in-memory allocator).
   - Helper `(m *Manager) backlogStore(absWS string) *docs.Store`: builds the Store and sets its id source to `m.idAlloc.NextID(<primary backlog dir>)` where primary = `m.primaryTreeFor(absWS)`:
     (a) absWS equals a registered project path → absWS;
     (b) a workstream registry entry has WorktreePath == absWS → projects.Resolve(ws.Project);
     (c) absWS is under m.worktreesRoot → match the `<projectDir>` path component against `workstream.SafeProjectDir(p.Name)` over registered projects (needed because SpawnWorkstream starts the session BEFORE registering the workstream, and covers Reopen after restart);
     (d) else absWS itself.
   - Use backlogStore in newSession (Deps.Docs), Manager.Backlog, CaptureBacklogItem.
4. Tests:
   - docs: allocator seeds from an existing backlog (e.g. max 0007 → issues 0008); never re-issues an id present in the primary tree; two Stores over two different (worktree) backlog dirs sharing the allocator get distinct ids under concurrent Create (goroutines, race-detector friendly); persistence: a fresh AllocatorFor over the same file continues past previously issued ids even when the primary tree lacks them; plain NewStore without an id source behaves as before (existing tests).
   - session: Manager-level test — register a project whose primary tree has a backlog; obtain stores for two distinct worktree paths of that project (via the resolution path (b) and/or (c)); concurrent Create from both yields distinct, correctly-seeded ids. Keep it filesystem-only (no live sessions needed) using project.NewMemory / workstream.NewMemory.
5. Verify: `go build ./... && go test ./internal/docs/... ./internal/session/... ./internal/server/...` and `go vet`. Note memory.md: some session tests are flaky — compare against HEAD if something unrelated fails.

### Starting points
- internal/docs/docs.go: nextID (l.354), lockFor/dirLocks (l.25), parseFile, normalizeID
- internal/session/session.go: Manager struct (l.1330), NewManager (l.1360), newSession Deps.Docs (l.1844), Backlog (l.2393), CaptureBacklogItem (l.2416), SpawnWorkstream registers workstream AFTER m.start (l.1660-1685)
- internal/workstream/registry.go: StateFile/DefaultWorktreesRoot/stateDir pattern; paths.go SafeProjectDir
- internal/daemon/serve.go l.87-110: persistent registry wiring site
- gotcha: allocator's primary-tree scan must not take the docs dirLocks — primary Store holds it during createLocked

## Work log
- 2026-08-07 plan: Goal: stop parallel worktrees from minting duplicate backlog ids by routing all daemon-side id allocation through one per-project, serialized, persistent counter, while keeping today's scan for daemon
…[truncated]
- 2026-08-07 context hints: 5 recorded with plan
- 2026-08-07 context hints: internal/docs/docs.go: nextID l.354, lockFor/dirLocks l.25-39, parseFile l.386, normalizeID l.433; internal/session/session.go: Manager struct l.1330, NewManager l.1360, newSession Deps.Docs l.1844, B
…[truncated]
- 2026-08-07 preload: 3 file(s), ~21 KiB seeded into implementer context
- 2026-08-07 implementer report: Implemented task 0249.  Changes: - Added `docs.IDAllocator`: process-shared per backing file, mutex-serialized, optionally persisted as an atomic JSON high-water-mark map keyed by absolute primary bac
…[truncated]
- 2026-08-07 review tier: single-opus — reviewers: sol
- 2026-08-07 review (sol): accept — The change satisfies task 0249. It introduces a process-shared, mutex-serialized per-primary-backlog allocator with durable high-water marks for persistent daemons, re-floors against the primary tree 
…[truncated]
- 2026-08-07 decision: accept — commit: Serialize backlog id allocation per project via a daemon-shared allocator (task 0249)
