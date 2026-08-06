---
id: "0248"
title: 'Workstream base branch: model it, and advance base by fast-forward only'
status: todo
priority: 1
created: "2026-08-06"
updated: "2026-08-06"
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


## Work log
