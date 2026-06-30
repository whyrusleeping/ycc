---
id: "0086"
title: 'Spec: document workstream concept + worktree decision'
status: todo
priority: 4
created: "2026-06-30"
updated: "2026-06-30"
depends_on:
    - "0078"
spec_refs:
    - Open questions
    - Persistence & remote sync
---

## Description
## Context
Final step of the parallel-workstreams design (see `docs/design/parallel-workstreams.md` §10.6). Keep the spec true (spec §1) by recording the decision and the new concept.

## Scope
- Revise spec §17 "Implementer isolation" to record the decision: git worktrees are adopted for parallel workstreams (the deferred revisit is now resolved).
- Add a short section describing the workstream concept, the daemon-side workstream registry, and the conflict-aware, review-gated merge flow.

## Acceptance criteria
- [ ] Spec §17 "Implementer isolation" updated to reflect the worktree decision.
- [ ] A spec section documents the workstream concept, registry, and merge flow consistent with the design doc.
- [ ] No stale "revisited only if/when" language remains where the decision is now made.

## Acceptance criteria

## Work log
