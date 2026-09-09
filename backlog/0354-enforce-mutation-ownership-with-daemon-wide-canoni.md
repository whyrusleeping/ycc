---
id: "0354"
title: Enforce mutation ownership with daemon-wide canonical-worktree leases
status: done
priority: 1
created: "2026-09-08"
updated: "2026-09-09"
depends_on: []
spec_refs:
    - §7.3 Subagents and asynchronous jobs
    - §8 Tools and access policy
    - §14.1 Parallel workstreams
---

## Description
The documented single-writer invariant exceeds current enforcement: job registries are session-scoped, spawn checks cover selected paths, and Write/Edit/foreground shell/background-shell creation do not all acquire a common worktree lease. Two sessions can bypass the intended boundary. Evidence: internal/session/session.go:2213; internal/jobs/jobs.go:239–272; internal/tools/worker.go:234–314,490–496. This is execution ownership, not the stricter role policy proposed in 0327; preserve direct work and do not automatically accept 0327.

## Acceptance criteria
- A daemon-level ownership service keys leases by canonical worktree, including path aliases.
- All potentially mutating execution paths honor ownership across sessions, direct coordinators, delegated agents, and shell jobs; define safe delegation/reentrancy instead of deadlocking a worker's own commands.
- Read-only work remains concurrent where enforcement is available; distinct workstreams can mutate independently.
- Acquisition is atomic, and release follows actual process/agent termination, including cancellation, failure, and shutdown.
- Refusals explain the owner and available wait/stop/workstream alternatives.
- Concurrency tests cover cross-session starts, file tools during delegated writes, shell paths, killed-but-not-yet-exited jobs, and separate worktrees.
- Document that unrestricted shell access remains outside a security sandbox.

## Outcome

Implemented daemon-owned canonical-worktree leases for file tools, shell commands, delegated agents, coordinator mutations, backlog RPCs, session Git initialization, and workstream operations. Synchronous worker commands reenter their scope; exclusive asynchronous child claims and active-run tracking prevent overlapping writes until actual exit, even after cancellation. Extra write roots lease their destination worktree. Ordinary reads remain concurrent; duplicate-ID repair defers while another scope owns the tree. Refusals identify the owner and wait/stop/workstream alternatives. Unrestricted shell access is explicitly not a security sandbox.

Verification: full Go suite and targeted concurrency/race tests passed; both comprehensive reviewers accepted. Existing session integration timing/environment flakes were observed separately during review. Unrelated pre-existing workspace changes are excluded from this commit.

Commit: `Enforce daemon-wide canonical-worktree mutation leases`.
