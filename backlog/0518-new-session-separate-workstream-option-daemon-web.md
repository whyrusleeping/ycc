---
id: "0518"
title: 'New Session: "Separate workstream" option (daemon + web), and make workstreams findable'
status: todo
priority: 2
created: "2026-11-10"
updated: "2026-11-10"
depends_on: []
spec_refs:
    - spec.md#14.1 Parallel workstreams
    - docs/design/parallel-workstreams.md
    - docs/design/web-client.md
---

## Description
**Why (2026-11-09 usage analysis):** the owner had never noticed workstreams. On web they're an unlabeled icon in the sidebar row. On iOS they're in the home **…** menu only when one project is selected, and iOS can't start one. Meanwhile parallel sessions in one shared tree keep colliding: commit refusals over other sessions' uncommitted work (0462), and the 2026-10-12 wheeler loop that spun on another session's uncommitted files. New Session (150+ uses a month) is where the owner decides how to run work, so the choice belongs there.

**Change:**
- New Session gets a **Separate workstream** toggle, off by default, with a one-line explanation (own worktree and branch; merges back using the project's integration setting). When it's on, submitting spawns a workstream with the prompt, optional backlog task and optional base ref, and opens its session.
- `SpawnWorkstreamRequest` currently takes only project/base_ref/task_id/prompt. Extend it, or route StartSession, so the toggle keeps everything else New Session offers: mode (chat/work), coordinator model override, preset, and attached pictures. Regenerate Go/web/Swift protos and commit all three.
- Workstream sessions are recognisable in the session list (a small branch marker) and link to their workstream.
- Give the sidebar's Workstreams entry a visible text label, or otherwise make it findable. Coordinate with 0497 (web sidebar declutter, in review) instead of undoing it.
- Analytics: `new_session.submit` gains `workstream=true|false`.

**Acceptance:**
- With the toggle on, a web New Session creates a workstream whose session starts with the same prompt/mode/model/preset/pictures it would have had otherwise.
- With the toggle off, behaviour is unchanged.
- The session list marks workstream sessions.
- Daemon tests cover the extended spawn fields.
- Web component tests plus a light/dark visual check pass. internal/web/dist is rebuilt and committed.
- The iOS counterpart is a separate task.

## Acceptance criteria

## Work log
