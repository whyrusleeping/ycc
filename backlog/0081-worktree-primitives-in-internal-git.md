---
id: "0081"
title: Worktree primitives in internal/git
status: done
priority: 4
created: "2026-06-30"
updated: "2026-07-02"
depends_on:
    - "0078"
spec_refs:
    - System architecture
    - Persistence & remote sync
---

## Description
## Context
First implementation step of the parallel-workstreams design (see `docs/design/parallel-workstreams.md` §5, §10.1). Extend the `git` helper with the worktree + merge primitives the workstream lifecycle needs.

## Scope
- Add to `internal/git`: `AddWorktree(dir, branch, baseRef)`, `RemoveWorktree(dir)`, `ListWorktrees()`, `PruneWorktrees()` wrapping `git worktree …`.
- Add merge helpers: a non-mutating `TrialMerge(branch)` that detects conflicts without mutating the base (via `git merge-tree` or `git merge --no-commit --no-ff` against a throwaway), and `Merge(branch, strategy)`.

## Acceptance criteria
- [ ] Worktree create/list/remove/prune helpers exist and wrap the corresponding git commands.
- [ ] `TrialMerge` reports clean vs. conflicting (with conflicted paths) without mutating the base branch/tree.
- [ ] `Merge` integrates a branch into base and reports the resulting commit (or a conflict).
- [ ] Unit tests over a temp repo cover create → commit-on-branch → trial-merge (clean & conflicting) → merge → remove → prune.
- [ ] `go build ./...`, `go vet ./...`, `go test ./...` pass.

## Acceptance criteria

## Plan

Extend internal/git with worktree + merge primitives per docs/design/parallel-workstreams.md §5/§6/§10.1.

1. New file internal/git/worktree.go (or extend git.go) with Repo methods:
   - AddWorktree(dir, branch, baseRef string) error — wraps `git worktree add <dir> -b <branch> <baseRef>`.
   - RemoveWorktree(dir string) error — `git worktree remove --force <dir>` (force so a dirty tree can still be discarded; document that).
   - ListWorktrees() ([]Worktree, error) — parse `git worktree list --porcelain` into a small Worktree struct {Path, Branch, Head, Detached/Prunable as useful}.
   - PruneWorktrees() error — `git worktree prune`.
2. New file internal/git/merge.go (or same file) with:
   - TrialMerge(branch string) (TrialMergeResult, error) — MUST NOT mutate the base branch/tree. IMPORTANT: host git is 2.34.1, which lacks `git merge-tree --write-tree` (needs ≥2.38). Implement via a throwaway detached worktree: `git worktree add --detach <tmpdir> HEAD` (tmpdir under os.MkdirTemp, outside the repo), run `git merge --no-commit --no-ff <branch>` there, collect conflicted paths via `git diff --name-only --diff-filter=U`, then `git merge --abort` (best-effort) and remove/prune the throwaway worktree (defer cleanup so failures don't leak worktrees). Result: {Clean bool, Conflicts []string}.
     (Optionally try `merge-tree --write-tree` first and fall back; not required — the throwaway-worktree path alone is fine and portable.)
   - Merge(branch string, strategy MergeStrategy) (MergeResult, error) — integrates branch into the current branch of r.Dir. MergeStrategy: at least MergeNoFF (default, `merge --no-ff -m ...`) and MergeFFOnly (`merge --ff-only`). On conflict: run `git merge --abort` so the base tree is never left conflicted (design §6), and return a MergeResult with Conflicts populated (or a typed error) — implementer's choice, but the conflicted-paths info must be reported and base restored. On success return the resulting commit sha (short ok).
3. Unit tests internal/git/worktree_test.go (temp repo via t.TempDir + Open):
   - create repo → AddWorktree on a branch from HEAD → ListWorktrees shows it → commit a change on the branch inside the worktree (set user.email/name in the temp repo config to be CI-safe; Open already does for fresh init).
   - TrialMerge clean case: reports Clean, and verify base branch HEAD + working tree unchanged afterward (rev-parse HEAD before/after, status --porcelain empty).
   - TrialMerge conflicting case: commit conflicting edits to the same file on base and branch; reports non-clean with the conflicted path; base still unmutated.
   - Merge clean: returns a commit sha; base contains the branch's file.
   - Merge conflicting: reports conflict, and base tree/HEAD is left clean (merge aborted).
   - RemoveWorktree then PruneWorktrees leave `git worktree list` with just the main tree.
4. Keep the wrapper thin and consistent with existing style (use r.run; add a run variant that tolerates expected non-zero exits where needed, e.g. merge conflicts). Doc comments referencing the design doc.
5. Verify: go build ./... && go vet ./... && go test ./... all pass.

### Starting points
- internal/git/git.go — existing Repo, Open, run() helper; follow this style
- docs/design/parallel-workstreams.md §5 (lifecycle), §6 (merge strategy), §10.1 (this task)
- host git is 2.34.1 — no `git merge-tree --write-tree`; use throwaway detached worktree for TrialMerge
- conflicted paths: `git diff --name-only --diff-filter=U` after a conflicted `merge --no-commit`

## Work log
- 2026-07-02 plan: Extend internal/git with worktree + merge primitives per docs/design/parallel-workstreams.md §5/§6/§10.1.  1. New file internal/git/worktree.go (or extend git.go) with Repo methods:    - AddWorktre
…[truncated]
- 2026-07-02 context hints: 4 recorded with plan
- 2026-07-02 context hints: internal/git/git.go — existing Repo, Open, run() helper; follow this style; docs/design/parallel-workstreams.md §5 (lifecycle), §6 (merge strategy), §10.1 (this task); host git is 2.34.1 — no `
…[truncated]
- 2026-07-02 implementer report: Implemented worktree + merge primitives in internal/git per the parallel-workstreams design (§5/§6/§10.1).  Files added: - internal/git/worktree.go — `Worktree` struct {Path, Head, Branch, Detach
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The change adds thin, well-documented worktree primitives (AddWorktree/RemoveWorktree/ListWorktrees/PruneWorktrees) and merge primitives (TrialMerge/Merge) to internal/git, consistent with the existin
…[truncated]
- 2026-07-02 decision: accept — commit: git: worktree + trial-merge/merge primitives for parallel workstreams (0081)
- 2026-07-02 usage: 15,437 tok (in 48, out 15,389, cache_r 368,488, cache_w 46,928) · cost n/a (unpriced)
  implementer: 8,504 tok (in 18, out 8,486, cache_r 92,737, cache_w 15,290) · cost n/a (unpriced)
  coordinator: 4,688 tok (in 16, out 4,672, cache_r 208,359, cache_w 17,380) · cost n/a (unpriced)
  reviewer:Claude: 2,245 tok (in 14, out 2,231, cache_r 67,392, cache_w 14,258) · cost n/a (unpriced)
