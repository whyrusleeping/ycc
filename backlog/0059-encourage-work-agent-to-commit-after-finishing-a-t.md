---
id: "0059"
title: Encourage work agent to commit after finishing a task (avoid leftover uncommitted backlog files)
status: done
priority: 3
created: "2026-06-29"
updated: "2026-06-29"
depends_on: []
spec_refs: []
---

## Description
## Problem

The work agent commits *before* marking a task as finished. When the task is then marked done, the act of updating the backlog task (status change, work-log entry, etc.) writes/modifies backlog files that are never committed. This leaves uncommitted backlog files lying around in the working tree after a task completes.

## Proposed change

Adjust the work agent's flow/prompting so that the commit happens *after* the task is marked finished, ensuring the final backlog state (status, work log) is included in the commit. At minimum, encourage the agent to commit again after finishing so no backlog files are left uncommitted.

## Acceptance criteria

- After the work agent finishes a task, the working tree has no leftover uncommitted backlog files related to that task.
- The commit includes the final backlog task state (e.g. status set to done, work-log entries).
- Existing pre-finish commit behavior still works (no regression in committing the actual code changes).

## Acceptance criteria

## Work log
- 2026-06-29 plan: Make `commit` the finalizing step that captures the full backlog state in one commit, and reorder the documented flow so the task is marked done before committing.  1. internal/orchestrator/orchestrat
…[truncated]
- 2026-06-29 implementer report: Implemented Task 0059: commit now finalizes the backlog state so no uncommitted backlog files are left after a task finishes.  Changes: 1. internal/orchestrator/orchestrator.go (commitTool): Moved the
…[truncated]
- 2026-06-29 review tier: single-opus — reviewers: Claude
- 2026-06-29 review (Claude): accept — The change correctly addresses the task. In commitTool, the work-log append was moved before the actual git commit so that the final backlog state (status set via update_task "done", plus the decision
…[truncated]
- 2026-06-29 decision: accept — commit fc32931: orchestrator: commit last so backlog state is captured, leaving a clean tree [0059]
- 2026-06-29 usage: 11,625 tok (in 68, out 11,557, cache_r 616,692, cache_w 36,079) · cost n/a (unpriced)
