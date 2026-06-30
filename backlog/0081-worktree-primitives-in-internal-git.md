---
id: "0081"
title: Worktree primitives in internal/git
status: todo
priority: 4
created: "2026-06-30"
updated: "2026-06-30"
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

## Work log
