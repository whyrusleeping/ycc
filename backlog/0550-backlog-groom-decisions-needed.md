---
id: "0550"
title: 'Backlog groom: decisions needed'
status: proposed
priority: 3
created: "2026-11-14"
updated: "2026-11-21"
depends_on: []
spec_refs: []
---

## Description
Unattended backlog grooms record here the questions that evidence could not settle. Each entry gives the task, the problem, the evidence, and the decision needed. An entry is dropped once it is answered.

## Flags

- **0548 (TestGetBacklogGroom fix, proposed, p2).**
  - Problem: the fix landed while the task was still proposed. The task is now stale.
  - Evidence: `4d38b28` (2026-11-19, "server: TestGetBacklogGroom expects done tasks to be excluded from groom churn") changed the fixture to expect `PendingChanged == 2`. `go test ./internal/server/ -run TestGetBacklogGroom` passed on 2026-11-21.
  - Decision needed: close 0548 as done with `4d38b28` as its outcome, or delete it. A groom does not change a proposed task's status without the user.
- **0511 (protect live session state from working-tree git operations, proposed, p3), after 0568 (shared-HEAD warnings, done).**
  - Problem: one of 0511's "harden" options is to have tools refuse git operations that rewrite `.ycc` paths. 0568 has since shipped a Bash-tool detector for HEAD rewrites, branch switches and stashes on a shared HEAD, but it does not look at `.ycc` session state. The two could share one detector.
  - Evidence: the 0511 Description options list; 0568's Outcome (Bash brackets commands with HEAD and refs/stash snapshots and appends a `[shared HEAD]` warning; spec §7.3).
  - Decision needed: extend the 0568 detector to git operations that would rewrite or roll back `.ycc` session state and narrow 0511 to relocating that state, or keep 0511 as filed.
- **0286 (TUI settings: edit review tiers and per-reviewer focus prompts, proposed, p4).**
  - Problem: the task is superseded. It asks for a tier editor in the TUI, which no longer exists, and the outcome already ships in the other clients.
  - Evidence: `6ce9e51` (2026-11-10, 0481) retired the TUI. `ListReviewTiers` / `UpsertReviewTier` / `RemoveReviewTier` exist in proto/ycc/v1/ycc.proto. The web `TierEditor.tsx` (`257af00`, 0416) and iOS `ReviewTiersView.swift` (`b38f56d`) edit tiers and slots with model, label, focus prompt and thinking.
  - Decision needed: close 0286 as done by those commits, or delete it.
- **0322 (review-loop circuit breaker and accepted-finding ledger, proposed, p1) and 0577, 0578, 0579 (review-cycle follow-ups from 0576, all now done).**
  - Problem: 0322 overlaps the newer tasks and partly conflicts with their scope. Its re-review handoff criterion overlaps 0577 (deliver coordinator verification evidence to reviewers). Its "persistent external evidence gate" and in_review terminal path overlap 0579 (ready-to-deploy verdict for operational criteria, `0fbd3c5`). Its round cap is the item 0578 (calibrate review-loop prompts) puts out of scope.
  - Evidence: the acceptance criteria of all four tasks. On 2026-11-22 the user kept `gpt-6.2-sol` as reviewer and asked not to be "too heavy handed" (session s_70ed48449817e056, event 974). 0578 then dropped round caps. Its work log records that rescope.
  - Decision needed: retire 0322, narrow it to what the newer tasks leave out (the structured finding ledger and reporting of unavailable reviewer slots), or keep it as is with its round cap.
- **0325 (transactional Patch tool, proposed, p3) and 0350 (Bash yield policy and structured shell outcomes, proposed, p3).**
  - Problem: both were accepted (todo) and had been attempted by work sessions, then moved back to proposed with no recorded reason. Their work logs read as if they were still queued work.
  - Evidence: `4d28017` (2026-10-03, a bulk commit "Add iOS Anthropic reconnect, work-loop fixes, and web client plans") changed both from `status: todo` to `status: proposed`. No session log in this project records the change. Neither is implemented; part of 0325's groundwork landed through 0348 (Read `source_revision`, Write `expected_revision`).
  - Decision needed: re-accept either task as todo, keep it proposed, or close it.
- **0593 (candidate tree for verification, review and commit, proposed, p2) and 0586 (line attribution of code files in shared worktrees, proposed, p3).**
  - Problem: near-duplicate. 0593's Description says it would absorb 0586, and one of its acceptance criteria is that 0586's criteria are met or 0586 is closed as superseded.
  - Evidence: the 0593 Description and acceptance criteria. 0586 was proposed in `bd15d9d` (with 0585, the non-blocking review note); 0593 came later from `docs/reports/shared-tree-concurrency-2026-11.md`.
  - Decision needed: merge 0586 into 0593 (delete 0586 or close it as superseded when 0593 is accepted), or keep both.

## Work log
- 2026-11-21 groom: kept the 0471 and 0520 flags, which still apply. Rewrote the 0548 flag, because its fix landed in `4d38b28`. Added the 0568/0511 overlap flag. Removed an empty Acceptance criteria heading.
- 2026-11-22 groom: kept the 0471, 0520, 0548 and 0568/0511 flags, which still apply. Added flags for 0286 (superseded by the web and iOS tier editors) and for 0322's overlap with 0577, 0578 and 0579.
- 2026-11-23 groom: dropped the 0471 flag (done in `8628914`; file restore split out to proposed 0589) and the 0520 flag (done in `d786bef` with pictures and New chat actions). Rewrote the 0568/0511 flag because 0568 is done, and noted that 0577–0579 are done in the 0322 flag. Added flags for 0325/0350 (moved from todo to proposed without a recorded reason) and the 0593/0586 merge. Kept the 0548 and 0286 flags.
