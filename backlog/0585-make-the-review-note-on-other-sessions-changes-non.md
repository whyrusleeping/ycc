---
id: "0585"
title: Make the review note on other sessions' changes non-blocking
status: done
priority: 2
created: "2026-11-22"
updated: "2026-11-22"
depends_on: []
spec_refs: []
---

## Description
Follow-up to 0580 (commit e72113d), at the user's direction. `spawn_reviewers` and `re_review` refuse to start reviewers when the snapshot takes whole code files that hold changes this session did not make (shared with another session, or already dirty before this session). In a shared worktree that refusal fires constantly, even for claims misattributed by a concurrent shell run, and its advice to wait for the other commit can deadlock parallel agents. The user wants parallel agents to work together more easily.

## Acceptance criteria
- Reviewers always start; the refusal and the `take_other_changes` parameter are removed.
- When the snapshot has such files, every reviewer round (initial, retained, and fresh) is told which files hold other sessions' changes and to judge only this task's changes. A defect or build/test failure caused by the other work is reported as attributable to it, not as a blocker for this task.
- The coordinator sees the same file list in the review result (and in the self-review result), with what it means at commit.
- A snapshot without such files is reviewed as before.

## Work log
- 2026-11-22: Done. The gate and `take_other_changes` are removed. `compactReviewDiffEvidence` now carries an "OTHER WORK IN THIS SNAPSHOT" note naming the files, so every reviewer round sees it. The note tells reviewers that other work's defects and build failures are not findings against this task and belong in the summary. Review results, background, foreground and self-review, add a commit note built from the evidence each reviewer actually inspected. Tests are in `internal/orchestrator/reviewgate_test.go`, and sol accepted in round 2. This was a manual scoped commit because the commit tool would also have taken 0461's misattributed `workloop` files and other sessions' `spec.md` hunks; that scope was verified on a clean HEAD worktree. Follow-up 0586 (line attribution for code files) is proposed.
