# Review cycles — November 2026

## Scope and evidence

Read-only analysis of this repository's session logs (`.ycc/sessions/*/events.jsonl`), June 28–November 21, 2026,
plus a reading of the review path in `internal/orchestrator`. Events from 23:50 UTC on November 21, when this
analysis started, are excluded. 274 sessions called `spawn_reviewers`. A *round* is one `spawn_reviewers` or
`re_review` call, including self-review tiers and calls that returned an error. Verdicts come from
`review_submitted` events, grouped by the review call that precedes them. Findings text comes from the
`spawn_reviewers`/`re_review` tool results and from background review job reports. The Wheeler section below repeats
the analysis on that project's logs. The task ids below name the long cycles that were read in full: 0443
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

## Wheeler

Wheeler's session logs (`~/code/wheeler/.ycc/sessions`, October 11–November 22) were analysed with the same method
and a cutoff of 00:20 UTC on November 22. Of 2,302 sessions, 1,807 are empty work sessions from the October 12
runaway loop; none reviewed anything. 240 sessions reviewed tasks in 250 session/task pairs. A task reviewed in two
sessions counts as two pairs, so a cycle that continues in a new session counts as two. Per pair, rounds rose from a
mean of 1.54 in October (3 of 131 pairs with four or more) to 2.14 in November (16 of 119, 13%). ycc counted the same
way: 2.11 in November, 4 of 36 pairs with four or more.

### Reviewer model version

`sol` resolved to `gpt-6.1-sol` until November 18 and to `gpt-6.2-sol` afterwards (the user config now names it).
Over November 17–22, `gpt-6.1-sol` asked for revision on 25 of 43 accept-or-revise verdicts in Wheeler (58%), and
`gpt-6.2-sol` on 37 of 48 (77%). In ycc, `gpt-6.2-sol` revised 27 of 30 (90%). ycc's long November cycles (0443,
0566, 0571, 0573) all ran on `gpt-6.2-sol`. The samples are small and share their weeks with other changes, so this
is a strong signal, not a controlled comparison. Wheeler's `gpt-6.1-sol` rate had already risen from
11–19% (October 12–25) to 42–82% (October 26–November 15); the cause of that earlier rise was not measured.

### Task size

Among reviewed sessions that committed, rounds per session grow with the committed diff, excluding generated code.
Wheeler: 1.19 rounds under 100 changed lines, 1.45 for 100–300, 1.69 for 300–800, 2.32 for 800–2,000, and 2.61 above.
ycc: 1.31, 1.07, 1.35, 2.05, and 3.50. Wheeler's median committed diff fell from 516 lines in October to 334 in
November while its rounds rose, so growth in task size does not account for the rise, though size still contributes.

### 5. Review before the acceptance criteria are met

Many Wheeler tasks have operational criteria: deploy, run a load, measure against a baseline, write a report.
Coordinators sent the code to review first, and reviewers returned "task completeness" as a major finding every
round until the evidence existed. 0175 (fast load of archived history) went through six rounds with both reviewers
within a day, all revised, and the session never committed. In rounds 4 and 6 neither reviewer reported a major code
defect; only operational acceptance criteria remained (the load and its counts, the operations procedure,
measurements, live-lag evidence). In one session, 0288 (Mastodon history tranche 2)
and then 0355 (adaptive account revisits) spent two and three rounds on missing deployment settings or production
measurements. A keyword pass found an unfinished-criteria or missing-evidence finding in 34 of Wheeler's 185 revise
rounds (18%), against 9 of 124 in ycc; a sample of 12 Wheeler matches were all relevant.

Reviewing code before deployment is sound, but the review has two outcomes: accept or revise. Some reviewers improvised
a staged acceptance ("accepted for the pre-rollout stage" in 0412, "accepted as an incremental change … not as
completion" in 0193); others revised every round.

### 6. Other sessions' work in the review snapshot

Concurrent sessions in Wheeler's shared tree edit the same files, and the scoped snapshot did not always match the
task's own work. Major and blocker findings that name another task, a session, a backlog path, or files absent from
the snapshot were checked by hand. The classification is not exhaustive, so the counts below are lower bounds:
- Another task's code hunks in the snapshot, at least 18 rounds in 8 tasks: 0428 rounds 1 and 4–8, 0309 rounds 1, 3
  and 4, 0287 rounds 1–2, 0302 rounds 2–3, 0008 rounds 1–2, 0019, 0222, and 0275. In 0428 (feed freshness alerts) the
  snapshot included partial 0414 and 0436 changes in `mastodon.go` and did not compile, and the session never
  committed. In 0309 the snapshot carried another task's import without the module it needs.
- Another session's backlog or memory edits in the snapshot, at least 8 rounds: 0423 rounds 1–3, 0222 rounds 1–3, and
  0344 rounds 3–4. In 0423 (parallel 4chan archive importer) every round flagged that the snapshot deleted another
  task's open backlog file and changed a third task's status, and the commit that closed 0423 still included those
  files.
- The task's own files missing from the snapshot, at least 4 rounds: 0178, 0192, 0193, and 0344 round 4, where the
  scoper had attributed the whole file to another session.

ycc had one such round (0521, regenerated outputs missing from the snapshot).

The snapshot header described most of the code cases. In 0428, 0309, 0287, 0302, and 0275 it listed the contaminating
file as "SHARED WORKTREE — also written by another session"; in 0019 and 0008 as "already dirty before this session;
earlier changes ride along". The logs do not show whether the other writer was still live,
which 0567 (refuse commits of files another live session also changed) requires before it refuses at commit. The
reviewer read the header; nothing asked the coordinator to settle those files before reviewing. Backlog leakage
belongs to the bookkeeping work in 0565 (stop commits sweeping in other sessions' uncommitted changes).

### Handoffs in Wheeler

18 Wheeler handoffs went to retained reviewers and were dropped (cause 1), with little visible effect. Wheeler
coordinators usually also put evidence in the work log, a report, or the docs, which are in the snapshot. One handoff
said so: "the evidence the first review missed is now in the scope".

## Proposed changes

- Deliver coordinator verification evidence to reviewers: post the `re_review` handoff and the latest implementer
  report in retain mode, and give `spawn_reviewers` an optional handoff for a direct-mode coordinator's verification.
  This is cause 1. It is a harness defect and the change with the most direct effect.
- Calibrate the review-loop prompts lightly, on the fixing side first (0578):
  - Coordinator and implementer: when fixing a finding, check affected paths for the same defect and fix any in
    scope. Coordinator: if successive revisions expose related defects, reconsider the approach before patching
    another case.
  - Reviewer: on the first pass, report all substantive findings that can be substantiated, grouping instances of the
    same defect; for a check the sandbox cannot run, judge whether the supplied evidence verifies the snapshot.
  - No severity definition: the split rounds read here were real defects the accepting reviewer missed, which does
    not justify narrowing what counts as major. In 0443 most later findings were in code the previous fix had added,
    so a different approach would have helped more than a more complete first review.
- Add a ready-to-deploy verdict for tasks with operational criteria: the snapshot is accepted for deployment or a run,
  the remaining criteria are named, and the next review skips the code only when the delta is evidence alone; changes
  to source, configuration, deployment files, migrations, or run scripts are reviewed as usual. The coordinator
  verifies the criteria it can check before deployment, then requests review and names the rest (cause 5).
- Warn the coordinator before reviewers start when the snapshot includes code files with changes the session did not
  make (shared with another session, or already dirty before it), naming the files and the alternatives (cause 6).
- Reviewer model: on 2026-11-22 the user chose to keep `gpt-6.2-sol` as the `sol` reviewer.

0577 (deliver coordinator verification evidence), 0578 (calibrate review-loop prompts), 0579 (ready-to-deploy
verdict), and 0580 (warn before reviewing changes the session did not make) are accepted todo tasks. The
measurements above can be rerun on later sessions to check the effect.
