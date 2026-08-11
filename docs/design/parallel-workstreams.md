# Design: parallel workstreams with git worktrees

> Status: accepted and implemented.

## Context

Two coding agents mutating one checkout can overwrite unstaged changes, stage each other's files,
and commit a mixed result. Serializing all work avoids races but prevents genuinely independent
tasks from progressing in parallel. Isolation must preserve normal git history and allow the
result to be inspected before entering the base branch.

## Decision

A workstream is a linked git worktree, ycc-owned branch, base commit, and work session. It belongs
to a registered project but is not itself a project. Worktrees live outside the primary checkout
under daemon state, while their session logs remain local to each worktree.

This makes the single-writer invariant local to a tree: one coordinator/implementer owns each
worktree, while different worktrees may mutate concurrently. The daemon serializes only its small
workstream registry and integration into a project's base branch.

Creation starts from a recorded commit rather than an uncommitted primary-tree snapshot. Project
configuration and other required bootstrap files may be copied explicitly, and configured
bootstrap commands run in the new worktree. Untracked or dirty primary-tree changes are not
silently imported.

Integration first performs a non-mutating preview. A clean result still requires the configured
integration policy; a conflict leaves the base tree untouched and retains the worktree for repair.
Successful integration or explicit discard removes the linked worktree and branch. The event log
records lifecycle transitions so every client observes the same state.

## Why worktrees

| Approach | Result |
|---|---|
| Linked worktree per stream | Shares object storage and repository config, gives each agent an independent index and filesystem, preserves ordinary branches and commits. |
| Branch switching in one checkout | Requires global serialization and remains unsafe around untracked/staged files and long-running processes. |
| Full clone per stream | Strong isolation but wastes object storage, complicates local-only refs/config, and makes cleanup and discovery harder. |
| In-memory/overlay filesystem | Hides normal tool behavior and makes git integration/debugging bespoke. |

Git worktrees are the smallest mechanism that gives filesystem and index isolation while keeping
git as the history/integration authority.

## Safety boundaries

- A workstream never advances the base branch merely because its session finished.
- Merge/integration conflicts never leave the primary checkout conflicted.
- The daemon validates registered paths and owns cleanup; arbitrary worktrees are not adopted by
  name alone.
- Parallelism is across trees. Background jobs inside one tree still obey one mutating writer.
- Workstream branches are implementation detail, but commits remain ordinary git objects that can
  be inspected and recovered before cleanup.

Automatic integration rationale and recovery policy are in
`docs/design/workstream-integration.md`; public behavior is in spec §14.1.
