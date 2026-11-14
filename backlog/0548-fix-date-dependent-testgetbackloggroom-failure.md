---
id: "0548"
title: Fix TestGetBacklogGroom's stale pending-churn expectation
status: proposed
priority: 2
created: "2026-11-13"
updated: "2026-11-13"
depends_on: []
spec_refs: []
---

## Description
`internal/server` TestGetBacklogGroom fails at HEAD with `GetBacklogGroom progress = pending 0x…, next 1790086400`. The message prints the `PendingChanged` pointer, not its value. `next` equals the expected `started + 24h`, so the failing check is the pending count.

The authoring agent first thought a hardcoded groom start date caused it. That is not the cause. On 2026-11-14 the test passed at d8d7702 and failed at 65ff85c ("Backlog groom: skip done tasks in churn and batch fill", 16 minutes later). From 65ff85c on, `backlogChurn` (internal/session/backloggroom.go) leaves done tasks out of the churn count. The test fixture still creates three tasks, one of them done, and expects `PendingChanged == 3`, but only 2 count now. The 0539 and 0546 Outcomes cite this failure as date-dependent.

## Acceptance criteria

- TestGetBacklogGroom's pending-churn expectation matches the done-task exclusion from 65ff85c, for example by expecting 2 or by making the fixture's churn tasks not done. It must pass whatever the run date.
- `go test ./internal/server/` passes.

## Work log
- 2026-11-14 groom: corrected the cause. The test passes at d8d7702 and fails at 65ff85c (both run in temporary worktrees), and the `backlogChurn` code explains why. Rewrote the title, description, and first criterion to match; the goal and the status (proposed) are unchanged.
