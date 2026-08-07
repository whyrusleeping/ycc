---
id: "0248"
title: 'Workstream base branch: model it, and advance base by fast-forward only'
status: done
priority: 1
created: "2026-08-06"
updated: "2026-08-07"
depends_on: []
spec_refs:
    - Parallel workstreams (git worktrees)
    - docs/design/workstream-integration.md#4. The integration queue
---

## Description

Today `MergeWorkstream` calls `git.Repo.Merge`, which runs `git merge` in the project's
**primary tree**, i.e. into whatever branch happens to be checked out there
(`internal/git/merge.go:116`). `workstream.Workstream` records only `BaseCommit` — there is
no base *branch* in the model. If the primary tree is on a feature branch, or is dirty, the
merge lands in the wrong place or fails midway.

Introduce the base branch as a first-class field and make integration a **fast-forward-only
ref advance** that never depends on the primary tree's checkout state:

- `[integration] base = "master"` in ycc.toml, defaulting to the repo's default branch;
  `SpawnWorkstream` records `BaseBranch` (alongside `BaseCommit`) in the registry and
  `WorkstreamInfo`.
- New git helpers: `DefaultBranch()`, `IsAncestor(a, b)`, `Rebase(onto)` (run in a worktree
  dir), and `AdvanceBranch(base, branch)` implementing:
  - base checked out in some worktree (usual case: primary) → `git merge --ff-only` there,
    requiring that tree clean; if dirty, return a distinct "base tree dirty" error and do
    nothing;
  - base not checked out anywhere → `git fetch . <branch>:<base>`.
  - Never `git update-ref` on a branch checked out elsewhere.
- `MergeWorkstream` keeps its accept gate but routes through rebase-in-worktree +
  `AdvanceBranch` instead of `git merge --no-ff` in the primary tree. `PreviewMerge`
  previews against the base branch, not primary HEAD.

## Acceptance criteria

- [ ] `[integration] base` parsed; workstreams record and report their base branch.
- [ ] Merging works when the primary tree is checked out on an unrelated branch (base is
      advanced without touching the primary working tree).
- [ ] Merging refuses (with a clear, distinct error) rather than mutating anything when the
      base tree is dirty and would be disturbed.
- [ ] Post-integration history is linear (branch rebased onto base, then fast-forward).
- [ ] Tests over a temp repo cover: base checked out / not checked out, dirty base tree,
      conflicting rebase, and a clean end-to-end advance.

## Plan

Goal: model the workstream base BRANCH first-class and make integration a rebase-then-fast-forward ref advance that never depends on (or disturbs) the primary tree's checkout state.

1. git helpers (internal/git):
   - `DefaultBranch()`: resolve the repo's default branch — try `git symbolic-ref refs/remotes/origin/HEAD` (strip prefix); else prefer a local `main` then `master` if they exist (`rev-parse --verify refs/heads/X`); else the current `symbolic-ref --short HEAD`; error if all fail (detached, no candidates).
   - `IsAncestor(a, b)`: `git merge-base --is-ancestor a b` → bool (exit 1 = false, other errors = err).
   - `RebaseOnto(dir, onto string)`: run `git rebase <onto>` in worktree dir. On failure collect conflicted paths (reuse conflictedPaths), `git rebase --abort` (best effort), and return a result {Clean bool, Conflicts []string} — worktree restored on conflict.
   - Exported sentinel `ErrBaseTreeDirty` (distinct "base tree dirty" error).
   - `AdvanceBranch(base, branch string)` (fast-forward-only ref advance):
     a. safety check: IsAncestor(base, branch) must hold, else error;
     b. ListWorktrees; if base is checked out in some worktree → require that tree clean (`status --porcelain` empty) else return ErrBaseTreeDirty wrapped with the dir; when clean run `git merge --ff-only <branch>` in that dir;
     c. base not checked out anywhere → `git fetch . <branch>:<base>` (updates the ref with no working tree; git itself refuses non-ff and checked-out targets).
     Never `git update-ref` on a checked-out branch. Return the resulting short sha of base.
   - Rework TrialMerge and DiffMergeBase to take an explicit base: `TrialMerge(base, branch)` checks out `--detach <base>` in the throwaway worktree (instead of HEAD); `DiffMergeBase(base, branch)` → `git diff <base>...<branch>`. Update all callers.

2. Config (internal/config/config.go): add `Integration struct { Base string `toml:"base,omitempty"` }` on Config as `[integration]`, plus a Registry accessor (e.g. `IntegrationBase() string`, mirroring Budget()). No extra validation needed (existence checked at spawn/merge time).

3. Model + spawn (internal/workstream/registry.go, internal/session/session.go):
   - Add `BaseBranch string `json:"base_branch,omitempty"`` to workstream.Workstream.
   - SpawnWorkstream resolves the base branch: if cfg.BaseRef names a local branch (`rev-parse --verify refs/heads/<ref>` succeeds) use it; else `[integration].base` from the config registry if set; else repo.DefaultBranch(). Record it in the registry entry and include `base_branch` in the workstream_created event data. If resolution fails, return a clear spawn error.
   - Add a small manager helper to resolve the effective base for a workstream at merge time (ws.BaseBranch, falling back for LEGACY entries with empty BaseBranch to config base / DefaultBranch), so pre-upgrade registry entries still merge.

4. Merge flow (internal/session/workstream_merge.go):
   - PreviewWorkstreamMerge: resolve base branch, TrialMerge(base, branch), DiffMergeBase(base, branch).
   - MergeWorkstream: keep mergeMu + in-flight + accept gate. Steps: trial-merge onto base (conflict → surfaceConflict, unchanged); !accept → NeedsAccept with diff vs base; accept → (a) VerifyUnderRoot the worktree path, then RebaseOnto(ws.WorktreePath, base) — conflicts → surfaceConflict (worktree left intact, rebase aborted); (b) AdvanceBranch(base, ws.Branch) — ErrBaseTreeDirty or other error propagates as an error WITHOUT mutating status/worktree; (c) on success proceed exactly as today: emit workstream_merged (commit = new base sha), SetStatus merged, stop session, preserve log, cleanupWorktree (safe branch delete now succeeds since base == branch tip; history is linear).
   - Update doc comments: rebase-then-ff replaces `git merge --no-ff` (design doc §4 step 3). Repo.Merge (MergeNoFF path) may become unused by this flow — keep the function if other callers exist, otherwise trim dead code as appropriate.

5. RPC surface: proto/ycc/v1/ycc.proto WorkstreamInfo gains `string base_branch = 12;`; regen Go (`~/go/bin/buf generate`) and Swift (`~/go/bin/buf generate --template buf.gen.swift.yaml`, remote plugins — needs network; if it fails, note it in the report and leave Swift untouched). server/toWorkstreamInfo sets BaseBranch. Map git.ErrBaseTreeDirty to CodeFailedPrecondition in server.workstreamError via errors.Is (do NOT reword existing string matches — the mapping is by string).

6. Tests:
   - internal/git: temp-repo tests for DefaultBranch (origin/HEAD, local-main fallback), IsAncestor, RebaseOnto clean + conflict (worktree restored), AdvanceBranch: base checked out & clean (ff advances), base checked out & dirty (ErrBaseTreeDirty, nothing mutated — ref unchanged, dirty file intact), base not checked out anywhere (fetch path advances the ref), non-ancestor refused.
   - internal/session (workstream_merge_test.go pattern, newWorkstreamManager): (a) end-to-end clean merge while the primary tree is checked out on an UNRELATED branch — base branch advanced, primary tree/branch untouched; (b) dirty base tree → distinct error, workstream stays in flight, worktree kept; (c) conflicting branch → conflict outcome, base untouched; (d) post-merge history linear (`rev-list --merges base..` empty / commit has one parent); (e) spawn records BaseBranch (and honors [integration] base when set).
   - Keep existing tests passing (they may need the new base-branch plumbing; the default temp repo's current branch will resolve as default branch, preserving behavior).

7. Verify: `go build ./... && go test ./internal/git/... ./internal/session/... ./internal/server/... ./internal/config/...`, then full `go test ./...` (known flaky: internal/session, internal/setup, internal/tools background bash — compare against HEAD before blaming).

### Starting points
- internal/git/merge.go — TrialMerge/Merge/DiffMergeBase, runAllow, conflictedPaths
- internal/git/worktree.go — ListWorktrees/AddWorktree/DeleteBranch/CountCommits
- internal/session/workstream_merge.go — MergeWorkstream/PreviewWorkstreamMerge/cleanupWorktree
- internal/session/session.go:1534-1665 — SpawnWorkstream + SpawnWorkstreamConfig
- internal/workstream/registry.go — Workstream struct (add BaseBranch)
- internal/config/config.go:742-800 — Registry accessor pattern (Budget/Notify)
- internal/server/workstream.go — toWorkstreamInfo, workstreamError (maps errors BY STRING; add errors.Is for new sentinel)
- proto/ycc/v1/ycc.proto:601 — WorkstreamInfo (next field number 12); regen: ~/go/bin/buf generate; Swift: --template buf.gen.swift.yaml (remote plugins, network)
- internal/session/workstream_test.go — newWorkstreamManager test harness
- docs/design/workstream-integration.md §4 step 3 — exact advance-base semantics

## Work log
- 2026-08-07 plan: Goal: model the workstream base BRANCH first-class and make integration a rebase-then-fast-forward ref advance that never depends on (or disturbs) the primary tree's checkout state.  1. git helpers (i
…[truncated]
- 2026-08-07 context hints: 10 recorded with plan
- 2026-08-07 context hints: internal/git/merge.go — TrialMerge/Merge/DiffMergeBase, runAllow(dir,...), conflictedPaths(dir); internal/git/worktree.go — ListWorktrees (Worktree has Branch field?), AddWorktree, DeleteBranch, C
…[truncated]
- 2026-08-07 preload: 6 file(s), ~42 KiB seeded into implementer context
- 2026-08-07 implementer report: Implemented task 0248 end to end.  Changes: - Added `[integration] base` config modeling and a concurrency-safe `Registry.IntegrationBase()` accessor. - Added `BaseBranch` to persisted workstreams, cr
…[truncated]
- 2026-08-07 review tier: single-opus — reviewers: sol
- 2026-08-07 review (sol): revise — The base-branch model, config/RPC plumbing, git helpers, and rebase/fast-forward integration are broadly implemented well, and the full Go test suite passes. However, the dirty-base guarantee is not m
…[truncated]
- 2026-08-07 revision: Fixed the dirty-base mutation ordering issue.  Changes: - Added `Repo.CheckBaseClean(base)` backed by a shared `checkBaseClean` helper that locates a checked-out base branch and returns an error wrapp
…[truncated]
- 2026-08-07 review (sol): accept — The revision addresses the dirty-base ordering defect by preflighting the checked-out base before rebasing and retaining the final cleanliness check in `AdvanceBranch`. The strengthened session test n
…[truncated]
- 2026-08-07 decision: accept — commit: Model workstream base branch; integrate by rebase + fast-forward-only advance (task 0248)  Workstreams now record a first-class BaseBranch (explicit local BaseRef > [integration] base in ycc.toml > re
…[truncated]
