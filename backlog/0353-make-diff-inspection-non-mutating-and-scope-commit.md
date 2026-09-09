---
id: "0353"
title: Make diff inspection non-mutating and scope commits to explicit changesets
status: done
priority: 1
created: "2026-09-08"
updated: "2026-09-09"
depends_on: []
spec_refs:
    - §10 Work orchestration
    - §14.1 Parallel workstreams
---

## Intent
Replace implicit whole-tree staging during diff inspection and commit with an explicit baseline/changeset contract, preserving unrelated staged, unstaged, and untracked work.

## Acceptance criteria
- Inspection never changes the user's index/worktree and includes intended tracked/untracked additions with explicit scope.
- Capture pre-existing state before mutation; identify inspected review/verification snapshots.
- Commit only the intended changeset, preserving unrelated working/index state.
- Refuse overlapping or ambiguous pre-existing changes with actionable guidance rather than guessed ownership.
- Cover clean/dirty baselines, staged versus unstaged state in one file, untracked additions, overlaps, failed commits, and reviewer preload.
- Document incremental delivery and conservative restrictions.

## Outcome
Implemented immutable temporary-index snapshots, persisted restart-safe baselines, scoped report/reviewer evidence and exact retrieval commands, literal root-anchored paths, and isolated commits with validation hooks, signing, exact-tree checks and HEAD compare-and-swap. Missing/stale baselines and dirty-path overlaps refuse safely. Existing unborn repositories still support chat without permitting unsafe review/commit. Delivery and recovery limits are documented in `plans/explicit-git-changesets.md`.

Both independent reviewers accepted. Full Go tests and focused Git/orchestrator race tests pass; exact task-only archive verification is used to exclude unrelated dirty work. Broader session race checks exposed an untouched model-switch race and an integration timing failure (the latter reproduced at HEAD); these are not changeset regressions. Snapshot pruning and read-only open optimization are proposed follow-ups 0374/0375; leases and finalization recovery remain 0354/0355.

Commit subject: `git: inspect and commit explicit task changesets without staging unrelated work`
