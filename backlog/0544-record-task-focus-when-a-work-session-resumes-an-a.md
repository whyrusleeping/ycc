---
id: "0544"
title: Record task focus when a work session resumes an already in_progress task (session title, loop focus, usage attribution)
status: done
priority: 3
created: "2026-11-11"
updated: "2026-11-11"
depends_on: []
spec_refs:
    - spec.md#20.2 Task attribution
---

## Description
A direct-implementation work session that resumes a task already `in_progress` never emits `task_focus` (only update_task→in_progress, spawn_implementer and switch_to_work do), so its title stays the canned work kickoff and workloop.json/usage get no focus. Seen in wheeler s_f1eb9904ee5723ed (0221) and s_f7a9ffc09010627d (0193).

## Acceptance criteria
- spawn_reviewers and re_review establish focus on their task's canonical id (deduped as today). Commit was dropped from scope: finalization's exactly-once/replayed event contract forbids an extra event, and review always precedes commit.
- Work-mode prompt tells the coordinator to set the task in_progress even when it already is, so focus is recorded at selection.
- Spec §20.2 states which actions establish focus.
- Tests cover the new emission sites.

## Outcome
spawn_reviewers/re_review call emitFocus(t.ID); workSystem prompt says to set in_progress even if already so; spec §20.2 lists focus-establishing actions. TestReviewEstablishesTaskFocus covers alias canonicalization and re_review dedupe; `go test ./...` passes.
