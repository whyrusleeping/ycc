---
id: "0355"
title: Make task finalization and commit failure recovery idempotent and truthful
status: done
priority: 1
created: "2026-09-08"
updated: "2026-10-10"
depends_on: []
spec_refs:
    - §6.2 Backlog
    - §10 Work orchestration
---

## Description

The commit tool calls Docs.Complete to mark done and compact the task before Repo.Commit. A git failure leaves the durable task completed even though the operation failed. Evidence: internal/orchestrator/orchestrator.go:974–985. Separate accepted implementation, verification evidence, successful commit, and finalized task without imposing a large new workflow.

## Acceptance criteria

- A failed commit cannot leave a newly misleading completed task or discard the information needed to resume safely.
- Repeated invocation after partial failure/restart is idempotent: recognize an already-created commit and avoid duplicate commits or compaction.
- Define recoverable intermediate state/order for task document changes, git commit, and event emission, preserving fail-stop log behavior.
- Preserve the requirement that the accepted committed tree contains the completed task outcome, without promising atomicity across git and event storage that does not exist.
- Test pre-commit hook failures, no-op/already-committed work, event-write failure, retry, and interrupted finalization.
- Coordinate with changeset scoping in 0353; do not stage unrelated work as a recovery shortcut.

## Outcome

Task finalization is journaled and recoverable: a failed commit restores the active task, and retries are idempotent (no duplicate commits or compaction; pending events are republished with a stable identity). If HEAD moves past a prepared, uninstalled commit, that commit is abandoned and the work is recommitted on the new HEAD. An installed commit that already has newer commits on top is now treated as installed. Only selected index paths that are still at the reviewed parent are advanced, to HEAD's versions. Later staging at a path, under it, or at a parent directory is preserved. The spec's finalization paragraph is updated. Tests cover hook failure, no-op, event failure, restart, divergence, and both file/directory transitions. The git, orchestrator, and docs suites pass.

Commit: finalization: recover installed commits under a descendant HEAD without clobbering later staging (0355)
