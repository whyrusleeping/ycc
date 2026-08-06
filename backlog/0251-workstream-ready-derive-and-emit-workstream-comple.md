---
id: "0251"
title: 'workstream_ready: derive and emit workstream completion state'
status: todo
priority: 2
created: "2026-08-06"
updated: "2026-08-06"
depends_on: []
spec_refs:
    - Parallel workstreams (git worktrees)
    - docs/design/workstream-integration.md#3. Lifecycle, extended
---

## Description

Nothing currently signals that a workstream has finished its work — its session just ends —
so no automation can attach to completion. Introduce readiness as an explicit, observable
state.

**Ready** = the workstream's session reached a terminal state AND the branch has commits
since `BaseCommit` AND the session did not end in error/blocked; when the workstream targets
a backlog task, that task must be `in_review` or `done`. A workstream that ends errored or
blocked goes to `needs_attention` instead and is never auto-integrated.

- Emit `workstream_ready` / `workstream_needs_attention` on the workstream's session stream
  via the existing `emitWorkstreamEvent`, with the reason recorded.
- Add the corresponding registry statuses and surface them in `WorkstreamInfo.status` (and
  the reducer/projection) so TUI and iOS rows can distinguish "still working", "ready", and
  "needs attention".
- Evaluate readiness where the session reaches its terminal state (and on
  `ReconcileWorkstreams`, so a daemon restart re-derives it).

## Acceptance criteria

- [ ] A workstream whose session ends with commits transitions to `ready` and emits the event.
- [ ] A workstream whose session errors/blocks, or that produced no commits, transitions to
      `needs_attention` (with reason) and never to `ready`.
- [ ] Statuses round-trip through the registry, `ListWorkstreams`, and the reducer.
- [ ] Readiness is re-derived after a daemon restart.


## Work log
