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
Task 0252 (integration queue: serialized rebase, verify, fast-forward) implemented the fast-path integration queue for `strategy = "rebase-ff"` only; `squash` and `merge-no-ff` are accepted by config validation but degrade auto mode to gate with a logged warning.

Implement actual execution of the other two strategies in the queue (after rebase + green verify pinned to the verified tip):
- `squash`: squash the workstream's commits into one commit on base
- `merge-no-ff`: merge commit onto base

Both must preserve the 0252 safety invariants: base never advanced past a failing verify, verified-content pinning (no unverified commits/tree changes can ride in), dirty base defers, conflicts → needs_attention with worktree intact.

## Acceptance criteria
- [ ] auto mode with strategy=squash and strategy=merge-no-ff integrates without degradation
- [ ] Pinned-verification invariants hold for both strategies (tests)
- [ ] Docs (spec §14.1 and the Policy modes section of docs/design/workstream-integration.md) updated to drop the rebase-ff-only caveat

## Work log
- 2026-11-22 groom: glossed 0252 and pointed the docs criterion at docs/design/workstream-integration.md's Policy modes section, since the design doc no longer has numbered sections. Checked that `IntegrationConfig.Strategy` (internal/config/config.go) still says the queue executes only rebase-ff; still proposed.
