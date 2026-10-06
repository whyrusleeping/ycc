---
id: "0423"
title: 'GetWorkingChanges: diff workstream sessions against base_commit instead of failing on a stale baseline'
status: proposed
priority: 3
created: "2026-10-06"
updated: "2026-10-06"
depends_on: []
spec_refs: []
---

## Description
Found during the web 0415 browser smoke: GetWorkingChanges for a workstream session fails once its agent has committed — `failed_precondition: changeset baseline … is stale: HEAD moved`. Every client (web, iOS) then shows an error for "Working changes" on a finished/in-flight-committed workstream, which is the most useful moment to look at its changes.

Proposal: for workstream sessions, when the commit-tool baseline is stale because the workstream branch advanced, fall back to diffing the worktree against the workstream's base_commit (clearly labelled as such in the response scope), rather than failing.

## Acceptance criteria
- [ ] Working changes on a workstream session that has committed returns the diff vs base_commit with an explanatory scope string.
- [ ] Non-workstream stale-baseline behavior is unchanged.

## Work log
