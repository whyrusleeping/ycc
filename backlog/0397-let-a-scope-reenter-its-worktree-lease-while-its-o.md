---
id: "0397"
title: Let a scope reenter its worktree lease while its own background children run; drop lease from git fetch
status: done
priority: 2
created: "2026-09-22"
updated: "2026-09-22"
depends_on: []
spec_refs:
    - §7.3 Subagents and asynchronous jobs
    - docs/design/async-jobs.md#Mutation safety
---

## Description
## Problem
Vals session s_b281eb34299ccc4c (task 0297 benchmarking): the coordinator's own background Bash (a benchmark) took an exclusive child claim that refused the SAME token's foreground Bash (even `true`, `ls; grep`, writes to /tmp), Write/Edit and backlog writes until exit, plus a second background job. The model responded by serializing every job with `wait` (~40 min idle). Separately, the periodic `daemon git fetch` took the full worktree lease every 3 min, causing random refusals (seq 285), although fetch only writes .git objects and refs/remotes.

The single-writer invariant exists to keep different actors from clobbering each other; an actor overlapping its own background process is its own coordination responsibility.

## Acceptance criteria
- Live child claims block other tokens (other sessions, mutating subagents, unsandboxed reviewers, parent after a handoff) until actual process exit, including after kill and after the scope's lifetime release — unchanged.
- The owning token may reacquire (foreground Bash, Write/Edit, backlog writes) and start additional background children while its children run.
- `daemon git fetch` no longer acquires the worktree lease.
- spec §7.3 and docs/design/async-jobs.md updated; tests updated to assert new same-token behavior and keep cross-token checks.

## Work log

## Outcome

- `workspacelease`: child claims are now a set per entry. The owning token reenters (foreground Bash, Write/Edit, backlog writes) and may start further children while its children run; foreign tokens are refused (conflict names the earliest live child) until actual exit, including after kill and lifetime release.
- New `Lease.Detach`, wired via `jobs.Job.SetHandoffHook` in `startBackgroundBash`: a child handed off to a parent refuses its creating scope too, so a later `send_to_implementer` turn cannot overlap it (preserves changeset attribution; caught by `TestImplementerExplicitWatcherHandoffToParent`).
- `daemon git fetch` no longer takes the worktree lease.
- spec §7.3 and async-jobs.md "Mutation safety" updated; lease/jobs tests rewritten for same-scope admission, added detach test.

Verification: full `go test ./...` green except `internal/docs TestRepositoryDocsConfig`, which fails only because of the local untracked `.ycc/config.toml` (passes on a clean HEAD archive with this diff applied); `-race` on workspacelease/jobs/tools lease tests green.
