---
id: "0059"
title: Encourage work agent to commit after finishing a task (avoid leftover uncommitted backlog files)
status: todo
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
