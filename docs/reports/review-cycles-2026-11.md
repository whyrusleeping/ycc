# Review cycles — November 2026

## Scope and evidence

Read-only analysis of this repository's session logs (`.ycc/sessions/*/events.jsonl`), June 28–November 21, 2026,
plus a reading of the review path in `internal/orchestrator`. Events from 23:50 UTC on November 21, when this
analysis started, are excluded. 274 sessions called `spawn_reviewers`. A *round* is one `spawn_reviewers` or
`re_review` call, including self-review tiers and calls that returned an error. Verdicts come from
`review_submitted` events, grouped by the review call that precedes them. Findings text comes from the
`spawn_reviewers`/`re_review` tool results and from background review job reports. The task ids below name the long cycles that were read in full: 0443
(session coordination setting), 0566 (line-attributed bookkeeping commits), 0571 (session search RPC), 0573 (iOS
session search UI), and 0388 (reviewer sandbox scratch).

## Measurements

Rounds per session: 1 round 181, 2 rounds 65, 3 rounds 19, 4 rounds 4, 5 rounds 1, 6 rounds 1, 7 rounds 3. Nine
sessions (3%) took four or more rounds. All nine are from August or later.

| Month | Sessions | Mean rounds | Sessions with ≥4 rounds |
| --- | ---: | ---: | ---: |
| 2026-06 | 48 | 1.19 | 0 |
| 2026-07 | 68 | 1.09 | 0 |
| 2026-08 | 59 | 1.59 | 2 |
| 2026-09 | 47 | 1.91 | 3 |
| 2026-10 | 16 | 1.56 | 0 |
| 2026-11 | 36 | 2.11 | 4 |

Revise rate by reviewer model, as revise verdicts over accept plus revise verdicts in `review_submitted` (5
`unknown` verdicts excluded): Claude/opus between 0% and 20% each month (6% in November). `sol`, a reviewer from
August on, 33% in August, 46% in September, 41% in October, and 55% in November.

81 rounds had two or more reviewers. 28 were unanimous accepts and 9 unanimous revises; 4 included an `unknown`
verdict. The other 40 were split, and in every one `sol` asked for revision while Claude/opus accepted. One revise
sends the whole change back for another round.

## Causes

### 1. `re_review` drops the coordinator's handoff in retain mode

`re_review` stores the `handoff` argument but posts only `reReviewPrompt` and the diff evidence to a retained
reviewer (`internal/orchestrator/orchestrator.go`, retain branch of `reReview`). The handoff reaches a reviewer
only when its context is rebuilt fresh. Retain is the default, and it does not forward the implementer's revised
report either. In direct mode `spawn_reviewers` has no channel for the coordinator's verification at all: the
reviewer is told "(no implementer report is available …)".

Coordinators sent a handoff in retain mode 8 times, in tasks 0353, 0564, and 0573, and none was delivered. In 0573
every round from 2 to 7 ended with the reviewer saying the Mac build and simulator check were "unverified" or "not
supplied". The handoffs for those rounds carried that evidence, and the round-7 handoff named a log file in the
workspace. Rounds 6 and 7 changed no code. Their only finding was the missing evidence the handoff contained.

This is a defect, not a calibration problem. It also prevents a coordinator from contesting a finding: in retain
mode any explanation it gives never reaches the reviewer.

### 2. Each fix patches the reproduced case, not the class

In 0443 the reviewer found a different TOML form in each of six rounds that the hand-written config editor
mishandled: a comment after a table header, a quoted inline key, a comment or string inside an inline table,
multiline strings, a `"""` inside a comment, an inline timestamp. Each revision fixed the case reproduced in the
previous round, and most new findings were in the code that fix added. Line-based text editing gave way to a
scanner only in round 5, and to a whole-document scanner in round 7. After round 7 the coordinator fixed the last
reported form and committed without another review. In 0571, a session-log lookup
that checked live sessions globally before the project was fixed in search, then in the workstream path, then in
`GetSessionView`. Each round found one more caller of the same lookup. 0566 followed the same pattern across
concurrent-write, staging, and recovery paths.

The prompts ask the coordinator to "fix substantive issues and re_review". Nothing asks it to look for the rest of a
finding's class, or to reconsider the approach when several rounds keep finding problems in the same area.

### 3. Reviewers grade the same snapshot differently

The reviewer prompt says to revise "only for blocker/major findings" but does not say what major means, and the 40
split rounds show the two models applying different thresholds to the same snapshot. The long splits read here do
not show `sol` over-grading. 0443's criteria required the Settings toggle to work, and each TOML form that left the
setting impossible to turn off failed that criterion even though the editor refused the write. 0388's criteria
required bounded scratch space and runtime, and the quota bypasses `sol` reproduced failed them; the final Claude
acceptance called the last bypass real. In 0566 opus accepted snapshots that lost another session's concurrent
edits. In 0571, reviewed by `sol` alone, search could expose another project's transcript. In these cases the
accepting reviewer had missed a criterion failure or graded it minor.

A shared definition of major would make the labels consistent between reviewers. It should not downgrade a
demonstrated criterion failure because it fails closed or needs unusual input. The larger lever for these cycles is
cause 2: finding the whole class, and changing approach, in fewer rounds.

### 4. Gates the reviewer cannot execute

0573's acceptance criteria require a build on the configured Mac. The reviewer sandbox cannot reach it, so the
reviewer can only inspect evidence the coordinator supplies, and cause 1 kept that evidence from it. The reviewer
prompt says what inspected evidence is, but not whether inspected evidence can satisfy a criterion that the reviewer
cannot execute itself.

## Proposed changes

- Deliver coordinator verification evidence to reviewers: post the `re_review` handoff and the latest implementer
  report in retain mode, and give `spawn_reviewers` an optional handoff for a direct-mode coordinator's verification.
  This is cause 1. It is a harness defect and the change with the most direct effect.
- Calibrate the review-loop prompts:
  - Reviewer: define blocker and major as a demonstrated acceptance-criterion failure, data loss or corruption, or a
    security exposure within the task's scope, whether or not it fails closed. Hardening beyond the task's criteria is
    a minor finding or a proposed follow-up.
  - Reviewer: in the first round, report every substantive finding, including other instances of the same problem.
  - Reviewer: for a gate the sandbox cannot execute, judge the supplied evidence and do not revise only because the
    reviewer could not reproduce it.
  - Coordinator and implementer: fix a finding's class, not just the reproduced instance. When successive rounds keep
    finding defects in the same area, change the approach (a real parser, a narrower scope, failing closed) instead
    of patching another case.

Both are filed as proposed backlog tasks: 0577 (deliver coordinator verification evidence) and 0578 (calibrate
review-loop prompts). The measurements above can be rerun on later sessions to check the effect.
