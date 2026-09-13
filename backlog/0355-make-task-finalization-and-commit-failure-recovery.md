---
id: "0355"
title: Make task finalization and commit failure recovery idempotent and truthful
status: in_review
priority: 1
created: "2026-09-08"
updated: "2026-09-09"
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

## Plan

Implement recoverable finalization around the explicit changeset commit API. Persist enough intent/original task state and commit identity outside the committed tree to distinguish pre-commit failure, an already-created commit, and pending event publication across restart. Ensure the committed tree includes the compact completed outcome while unsuccessful git attempts restore a truthful resumable task, and retries never broaden scope or duplicate commits. Preserve event-log fail-stop checks; document the actual git/event ordering and non-atomic boundary. Add targeted failure/retry/restart tests, inspect scoped diffs, obtain comprehensive review because this changes git/persistence recovery, and commit only this task's changes, preserving the existing dirty workspace.

### Starting points
- internal/orchestrator/orchestrator.go:commitTool, Deps.changeset
- internal/git/changeset.go:Repo.Commit (HEAD update then index replacement can partially succeed)
- internal/docs/docs.go:Store.Complete
- Existing unrelated dirty tree backed up at /tmp/ycc-0355-baseline/{tree.tar,pre.diff}; preserve it.

## Work log
- 2026-09-09 coordinator handoff: Three implementation/review rounds completed; claude accepts, sol still requests a supported recovery transition after a created commit definitively diverges from HEAD. Current code truthfully restores the task but retains a prepared journal/commit identity that repeatedly refuses finalization against the changed parent. Next step: define safe reconciliation/abandonment without narrowing original scope; test divergence through reconciliation to successful finalization. Do not mark done or commit until resolved. Targeted docs/git/orchestrator tests and diff-check pass; implementer reports full Go suite and targeted race pass. Task-owned files: internal/docs/docs{,_test}.go, internal/git/{git.go,changeset.go,changeset_test.go}, internal/orchestrator/{orchestrator.go,finalization.go,finalization_test.go}, four completion-guidance hunks in prompts.go, and finalization paragraph in spec.md. All other pre-existing changes must remain excluded; pre-task snapshot is /tmp/ycc-0355-baseline/tree.tar and pre.diff. Optional later issues: finalization-journal lifecycle for reopened tasks and recovery artifact pruning.
- 2026-09-09 implementer report: Implemented recoverable, idempotent task finalization without committing or disturbing the existing dirty/staged workspace.  Changes: - Added a repository-private finalization journal that retains the
…[truncated]
- 2026-09-09 revision: Revised Task 0355’s journaled finalization recovery and did not commit or alter the existing index.  Changes: - Added explicit git commit-state classification (`uncreated`, durably `created`, and HE
…[truncated]
- 2026-09-09 revision: Revised Task 0355’s recovery phase machine while preserving the existing staged/dirty workspace and making no commit.  Changes: - Bound every finalization journal to the original changeset baseline.
…[truncated]
