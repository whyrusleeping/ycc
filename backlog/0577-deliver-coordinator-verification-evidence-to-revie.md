---
id: "0577"
title: Deliver coordinator verification evidence to reviewers
status: proposed
priority: 2
created: "2026-11-21"
updated: "2026-11-21"
depends_on: []
spec_refs: []
---

## Description
Found by 0576 (review-cycle investigation, `docs/reports/review-cycles-2026-11.md` cause 1).

In retain mode (the default) `re_review` stores the coordinator's `handoff` but posts only `reReviewPrompt` and the diff evidence, so the reviewer never sees it; the latest implementer report is not forwarded either. In direct mode `spawn_reviewers` has no channel for the coordinator's own verification, and the reviewer is told no implementer report is available. Coordinators sent 8 retain-mode handoffs (tasks 0353, 0564, 0573) and none arrived. In 0573 (iOS session search UI) rounds 2–7 each ended with "Mac build and simulator check unverified" although each handoff carried that evidence; rounds 6 and 7 changed no code.

## Acceptance criteria
- A retained reviewer's re-review prompt includes the coordinator's `handoff` and the latest implementer report when present.
- `spawn_reviewers` accepts an optional handoff whose text reaches every initial reviewer, so a direct-mode coordinator can supply its verification evidence.
- Tool descriptions no longer say the handoff applies to fresh mode only.

## Work log
