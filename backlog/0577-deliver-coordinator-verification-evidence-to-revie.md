---
id: "0577"
title: Deliver coordinator verification evidence to reviewers
status: done
priority: 2
created: "2026-11-21"
updated: "2026-11-22"
depends_on: []
spec_refs: []
---

## Description

Found by 0576 (review-cycle investigation, `docs/reports/review-cycles-2026-11.md` cause 1).

In retain mode (the default) `re_review` stores the coordinator's `handoff` but posts only `reReviewPrompt` and the diff evidence, so the reviewer never sees it; the latest implementer report is not forwarded either. In direct mode `spawn_reviewers` has no channel for the coordinator's own verification, and the reviewer is told no implementer report is available. Coordinators sent 8 retain-mode handoffs (tasks 0353 (non-mutating diff inspection and changeset-scoped commits), 0564 (experimental project notice board), 0573 (iOS session search UI)) and none arrived. In 0573 rounds 2–7 each ended with "Mac build and simulator check unverified" although each handoff carried that evidence; rounds 6 and 7 changed no code. Wheeler (another project run with ycc, whose session logs the report also covers) dropped 18 such handoffs with little visible effect, because its coordinators also wrote the evidence into work logs, reports, or docs in the snapshot.

## Acceptance criteria

- A retained reviewer's re-review prompt includes the coordinator's `handoff` and the latest implementer report when present.
- `spawn_reviewers` accepts an optional handoff whose text reaches every initial reviewer, so a direct-mode coordinator can supply its verification evidence.
- Tool descriptions no longer say the handoff applies to fresh mode only.

## Outcome

Retained re_review now posts the coordinator's handoff and the latest implementer report (when present) with the delta evidence, and its context-budget check counts them. spawn_reviewers accepts an optional bounded handoff that seeds every initial reviewer and survives first-round context-error recovery. Tool descriptions describe handoff delivery in both modes; spec §7.3 updated. New test TestReviewersReceiveCoordinatorHandoffInDirectAndRetainedRounds; go test ./... passes; one reviewer (sol) accepted.

Commit: Deliver coordinator handoff and implementer report to reviewers in every round
