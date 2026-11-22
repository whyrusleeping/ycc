---
id: "0580"
title: Warn before reviewing a snapshot that includes changes this session did not make
status: done
priority: 3
created: "2026-11-22"
updated: "2026-11-22"
depends_on: []
spec_refs: []
---

## Description
Found by 0576 (review-cycle investigation, `docs/reports/review-cycles-2026-11.md` cause 6, from Wheeler's session logs). Related to 0565 (stop commits sweeping in other sessions' uncommitted changes) and 0567 (refuse commits of files another live session also changed, done).

The review snapshot takes a code file whole, including hunks this session did not write. Hand-checked findings show at least 18 Wheeler rounds, in 8 tasks, with another task's code hunks in the snapshot. In 0428 (feed freshness alerts) six of eight rounds had a blocker because the snapshot included partial 0414 and 0436 changes to `mastodon.go` and did not compile; the session never committed. The snapshot header listed the file as "SHARED WORKTREE — also written by another session" in 0428, 0309, 0287, 0302, and 0275, and as "already dirty before this session" in 0019 and 0008. The reviewer reads that header, but `spawn_reviewers` starts reviewers anyway, and 0567's commit refusal applies only while the other writer is live.

Other sessions' backlog or memory edits in the snapshot (at least 8 Wheeler rounds, in 0423, 0222, and 0344) are bookkeeping leakage for 0565, not this task.

## Acceptance criteria
- When the scoped snapshot includes code files with changes this session did not make (shared with another session, or already dirty before this session), `spawn_reviewers` and `re_review` tell the coordinator before reviewers start, naming the files and the alternatives: wait for the other commit, use a workstream, or take the file whole deliberately and name the other work.
- A snapshot without such files is reviewed as before.

## Work log
- 2026-11-22: Done. `spawn_reviewers` and `re_review` refuse before any reviewer starts when the snapshot holds code files another session also changed or that were dirty before the session. The refusal names the files and the alternatives; `take_other_changes: true` on each call proceeds, and a self-review result carries the note. Tests are in `internal/orchestrator/reviewgate_test.go`. Review (sol) accepted the gate fixes. Its remaining findings were only about other sessions' work in the shared snapshot (0461 `workloop*.go`, `spec.md` hunks), so the user chose a manual scoped commit; that scope was verified to build and test on a clean HEAD worktree.
