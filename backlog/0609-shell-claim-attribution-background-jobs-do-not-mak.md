---
id: "0609"
title: 'Shell claim attribution: background jobs do not make their session a co-claimant of other sessions'' writes (narrowest window wins)'
status: done
priority: 2
created: "2026-11-24"
updated: "2026-11-25"
depends_on: []
spec_refs:
    - spec.md#7.3
---

## Description
Current state: todo. Accepted by the user 2026-11-24 in session s_5a6bb9c25a12632a, after the UX review of the live wheeler work loop; the user's framing was that long-running background jobs should not carry broad claims. The three rule changes below are the authoring agent's proposal, accepted as the task's direction; the one-minute cap is a starting value to confirm during the work.

Spec §7.3 today: running shells are registered per worktree, and a path that changed during a command with no definite claim gets an ambiguous claim for the command's scope and for every scope whose shell ran meanwhile. The path is then in every holder's changeset and a commit refuses it while another holder is live. A shell write becomes definite only when no other scope's shell runs, or through a file-tool write or a commit.

A background job is registered from start to exit (`startBackgroundBash` in `internal/tools/worker.go`: `snapshotShell` before `cmd.Start`, `claimShellChanges` after `cmd.Wait`). In a parallel work loop each session holds a job for hours, so every session counts as concurrent with every command, and no shell write can settle.

Observed 2026-11-24 in wheeler's `.git/ycc/claims.json` during loop `loop_d074bc58` (4 slots): 16 ambiguous paths with 3 to 7 claimants each. The 0342 monitor session held 16 claims, all ambiguous; the 0317 soak session, waiting on one job for three hours, held 13, all ambiguous. Neither had written those files. Five more sessions held three ambiguous claims each (Makefile, `docs/design/research.md` and others); four of them no longer appeared in `ycc list`, so no coordinator could release those. Loop sessions spent tool calls on `release_claim`, and 5 commits were hand-scoped with `git commit` (the loop's "landed manually" list).

Proposed rule, "the narrowest window wins":
1. A background job does not count as concurrent for another scope's foreground command. A path that changed during a foreground command, with no definite claim, is claimed definitely for that command's scope. Two overlapping foreground commands of different scopes stay ambiguous as today. The rule applies to foreground commands shorter than a cap (starting value one minute); longer ones keep today's behaviour.
2. When a background job exits, it claims the paths that changed during its run and that no scope claims. If another scope's background job overlapped, such a path is ambiguous between those jobs' scopes only.
3. A scope's ambiguous claims are released when its session ends.

Known cost: a job that writes a tracked or untracked-unignored file during another session's short command has that file claimed definitely by the other session. Recovery path to verify: that session releases it, and the job's exit then claims it, since a released path has no definite claimant.

Related: 0593 (candidate tree for verification, review and commit in shared worktrees, proposed) and 0592 (generated outputs as derived files, proposed) address what a commit takes; this task changes which paths a session is said to have written. 0603 (show ambiguous claims apart from definite ones) depends on this task and covers the display of the ambiguous claims that remain.

## Acceptance criteria
- Spec §7.3 states the new rule, including the cap and the session-end release.
- A session whose only running shell is a background job gains no claim from another session's foreground command (test in `internal/tools`).
- A foreground shell write under the cap is definite for its scope while other scopes' background jobs run, and the commit tool takes it without refusal on that account (test).
- Two overlapping foreground commands of different scopes still yield ambiguous claims (existing behaviour kept, test).
- A background job's exit claims unclaimed paths changed during its run; with another scope's job overlapping they are ambiguous between the two scopes only (test).
- Ending a session releases its ambiguous claims, in memory and in the persisted `claims.json` (test).
- The recovery path in "Known cost" is covered by a test.
- The `[shell attribution]` notice text and the `release_claim` guidance match the new rule.

## Outcome

A background job and another session's foreground command under one minute no longer count as concurrent (`workspacelease.EndShell`), so a short command's write is definite for its session and its commit takes it while other jobs run. Two foreground commands, two jobs, or a job and a longer command stay ambiguous. A job's exit claims unclaimed paths changed during its run, ambiguously only with overlapping jobs; it leaves to a still-running short command the paths whose state differs from that command's starting snapshot, so the short window wins in either exit order. Shell attributions run one at a time (`AttributeShell`). Ambiguous claims are tagged with the session instance (token incarnation). `Manager.Stop` and reclaim call `EndScope`, which drops that instance's ambiguous claims in memory and in `claims.json` and records no new ones for it; a reopened instance keeps its own. Spec §7.3, the `[shell attribution]` notice and the `release_claim` guidance were updated. Tests cover each criterion in workspacelease, tools, orchestrator and session; the one-minute cap was kept. Landed with a hand-scoped commit because spec.md also held 0605's uncommitted §9.1 hunk.

Commit: 0609: shell claim attribution — narrowest window wins; session end releases its ambiguous claims
