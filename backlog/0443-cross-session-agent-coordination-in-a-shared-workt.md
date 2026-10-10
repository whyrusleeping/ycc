---
id: "0443"
title: Cross-session agent coordination in a shared worktree (activity view + direct messages)
status: proposed
priority: 3
created: "2026-10-09"
updated: "2026-10-09"
depends_on:
    - "0440"
spec_refs:
    - spec.md#7.3
---

## Description
## Why
After 0440, several sessions and agents can mutate one worktree at the same time. Writes are attributed per session through path claims, and overlaps show up only at review and commit time. Agents still can't see each other while they work, and they have no way to coordinate. Two sessions can drift into the same files and find out only when their change manifests show "SHARED WORKTREE".

## Idea (user's "fun idea", not yet required)
1. **Awareness (cheap):** a read-only `worktree_activity` tool, and/or a note added to Edit/Write results the first time a session touches a path another live scope has claimed. It lists the other live sessions in this worktree, their focus task, and the paths they've claimed. The claims registry (`workspacelease.Service.Claims`) and the session focus events already hold this data.
2. **Direct messages:** a `message_session(session_id, text)` tool that delivers a note into another live session's conversation, the way background-job completions are injected at a checkpoint. It's tagged with the sender's session and task, and the receiving session can reply. Use cases: "I'm refactoring internal/tools/worker.go, hold off," or "I need X exported from your package." This needs:
   - rate limiting;
   - user visibility in all clients;
   - a rule that it never wakes a session the user paused.
3. Optional: soft path reservations ("I'm about to edit these files") that surface as warnings, never hard locks.

## Acceptance (draft)
- An agent can find out which other sessions are active in its worktree and what they're touching, without hitting a lock.
- Messages between sessions are durable events, visible to the user, and delivered at the receiver's next checkpoint.
- Nothing here blocks a write. Coordination is advisory, consistent with 0440.

## Acceptance criteria

## Work log
