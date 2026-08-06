---
id: "0252"
title: 'Integration queue: serialized rebase → verify → fast-forward, with auto/gate/manual modes'
status: todo
priority: 2
created: "2026-08-06"
updated: "2026-08-06"
depends_on:
    - "0248"
    - "0251"
spec_refs:
    - Parallel workstreams (git worktrees)
    - docs/design/workstream-integration.md#4. The integration queue
    - docs/design/workstream-integration.md#5. Configuration
---

## Description

Build the per-project integration queue described in
`docs/design/workstream-integration.md` §4. One integrator per project, **serialized** (it
subsumes today's `mergeMu`), fed by `workstream_ready` (task 0251).

Per attempt, the **fast path only** (the agent path is task 0253):

1. In the workstream's worktree: `git rebase <base>`; then run `[integration] verify`.
2. Both succeed → advance base by fast-forward (task 0248's `AdvanceBranch`), preserve the
   session log, remove worktree, delete branch, prune, status `merged`, emit
   `workstream_merged`, notify.
3. Rebase conflicts or verify fails → status `needs_attention` + notification, worktree and
   branch left intact (task 0253 later inserts the agent attempt here).
4. Base tree dirty → defer and notify; never touch the user's uncommitted work.

Config (`[integration]`): `mode = auto | gate | manual` (**auto** is the default), `verify`,
`strategy = rebase-ff | squash | merge-no-ff` (default `rebase-ff`), `max_parallel`.

Guardrails:
- **`auto` requires a configured `verify`**; without one it degrades to `gate` with a logged
  warning. Never auto-merge unverified code.
- `gate` keeps the existing accept-diff flow but marks the stream as awaiting acceptance.
- `manual` is exactly today's behaviour.
- `max_parallel` caps concurrently active workstreams at spawn time (0 = unlimited); it is
  also the stand-in for the deferred resource-lease work.

## Acceptance criteria

- [ ] Two ready workstreams integrate sequentially; the second rebases onto a base that
      already contains the first.
- [ ] Clean + green integrates with no model calls at all (fast path costs zero tokens).
- [ ] Conflict or red verify leaves base untouched, the worktree intact, and the stream in
      `needs_attention` with the failing output recorded.
- [ ] `auto` without `verify` degrades to `gate`.
- [ ] `max_parallel` refuses spawns past the cap with a clear error.
- [ ] `mode = manual` reproduces current behaviour exactly (regression).


## Work log
