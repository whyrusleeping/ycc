---
id: "0253"
title: 'integrate mode: agent resolves rebase conflicts and verify failures in the worktree'
status: todo
priority: 2
created: "2026-08-06"
updated: "2026-08-06"
depends_on:
    - "0252"
spec_refs:
    - Parallel workstreams (git worktrees)
    - docs/design/workstream-integration.md#4. The integration queue
---

## Description

The integration queue's fast path (task 0252) handles clean rebase + green verify. Everything
else — merge conflicts, a test that breaks only after rebasing onto the advanced base — should
be handled by an agent, not by interrupting the user (that interrupt is the cost
auto-integration exists to remove).

Add an **`integrate` mode**: a coordinator session **scoped to the workstream's worktree**
(never the primary tree — the worktree is the correct blast radius) with the worker toolset
plus git. Seed prompt names the base branch, the conflicted paths and/or the failing verify
output, and the contract: resolve or fix, re-run `verify`, then request integration; if the
call isn't yours to make, `report_blocked`.

- Bounded by `[integration] agent_attempts` (default 1).
- Success → the queue re-runs verify itself and advances base; the agent never touches base.
- Failure/blocked/attempts exhausted → `needs_attention` + notification, worktree intact.
- The `integrate` session is a normal session: it streams, is drillable from the workstream
  row, and its transcript is preserved on cleanup.

## Acceptance criteria

- [ ] A conflicting rebase is resolved by the integrate session and the workstream merges,
      with the resolution visible in its transcript.
- [ ] A verify failure introduced by base drift is fixed and merged.
- [ ] An unresolvable case ends in `needs_attention` after `agent_attempts`, base untouched.
- [ ] The integrate session cannot advance base itself (the daemon owns that step).
- [ ] Spec §14.1 wording flips from "planned" to the implemented behaviour once 0252+0253
      land.


## Work log
