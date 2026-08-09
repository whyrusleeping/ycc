---
id: "0300"
title: Execute squash / merge-no-ff strategies in the auto-integration queue
status: proposed
priority: 4
created: "2026-08-08"
updated: "2026-08-08"
depends_on:
    - "0252"
spec_refs: []
---

## Description
Task 0252 implemented the fast-path integration queue for `strategy = "rebase-ff"` only; `squash` and `merge-no-ff` are accepted by config validation but degrade auto mode to gate with a logged warning.

Implement actual execution of the other two strategies in the queue (after rebase + green verify pinned to the verified tip):
- `squash`: squash the workstream's commits into one commit on base
- `merge-no-ff`: merge commit onto base

Both must preserve the 0252 safety invariants: base never advanced past a failing verify, verified-content pinning (no unverified commits/tree changes can ride in), dirty base defers, conflicts → needs_attention with worktree intact.

## Acceptance criteria
- [ ] auto mode with strategy=squash and strategy=merge-no-ff integrates without degradation
- [ ] Pinned-verification invariants hold for both strategies (tests)
- [ ] Docs (spec §14.1 + design doc §5) updated to drop the rebase-ff-only caveat

## Work log
