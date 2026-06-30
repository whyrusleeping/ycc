---
id: "0083"
title: Workstream merge/integration flow with conflict surfacing
status: todo
priority: 4
created: "2026-06-30"
updated: "2026-06-30"
depends_on:
    - "0082"
spec_refs:
    - Persistence & remote sync
    - Session & event log
    - Interaction levels
---

## Description
## Context
Third step of the parallel-workstreams design (see `docs/design/parallel-workstreams.md` §6, §10.3). Implement integrating a completed workstream back to base with explicit, conflict-aware, review-gated merges — never silent auto-resolution.

## Scope
- Implement the §6 flow: trial-merge to detect conflicts; under autonomous level auto-merge clean workstreams, under interactive/judgement levels surface the integrated diff as an accept gate; sequential reconciliation across workstreams (each rebased/merged against the latest base).
- Conflict path: emit a `workstream_conflict` event listing conflicted paths/hunks and stop, offering (a) a resolve-mode session in the worktree or (b) handing off to the user. The base branch is never left conflicted.
- Cleanup on success: `git worktree remove`, branch delete, prune.

## Acceptance criteria
- [ ] Clean trial-merge → merged per interaction level (auto under autonomous; accept-gated otherwise).
- [ ] Conflicting trial-merge → `workstream_conflict` event with conflicted paths; base branch untouched.
- [ ] Merges are sequential across workstreams (second sees the first's changes).
- [ ] New event types (`workstream_created/merged/conflict/discarded`) + reducer handling.
- [ ] Successful merge cleans up the worktree + branch.
- [ ] Tests cover clean + conflicting integration; build/vet/test pass.

## Acceptance criteria

## Work log
