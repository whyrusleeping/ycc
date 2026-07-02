---
id: "0117"
title: Preserve workstream session transcripts after merge/discard removes the worktree
status: todo
priority: 5
created: "2026-07-02"
updated: "2026-07-02"
depends_on:
    - "0085"
spec_refs: []
---

## Description
## Context
Task 0085's Workstreams panel drills into a workstream's session via ResumeSession. For a live workstream this works; but once a workstream is merged or discarded, its worktree (and the session log under `<worktree>/.ycc/sessions/<id>/events.jsonl`) is removed, so drilling into a terminal row surfaces a transient error notice instead of the transcript.

## Scope
Preserve (or relocate) a workstream session's event log at merge/discard time so its transcript remains viewable from the Workstreams panel / session browser afterwards — e.g. copy the log into the primary workspace's `.ycc/sessions/` before the worktree is removed, or persist it daemon-side.

## Acceptance criteria
- [ ] After merging or discarding a workstream, its session transcript is still viewable (panel drill-in or session browser) instead of erroring.
- [ ] No regression to the merge/discard cleanup flow; build/vet/test pass.

## Acceptance criteria

## Work log
