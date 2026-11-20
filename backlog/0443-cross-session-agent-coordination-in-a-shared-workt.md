---
id: "0443"
title: 'Experimental cross-session messages: list_sessions and message_session tools'
status: todo
priority: 3
created: "2026-10-09"
updated: "2026-11-20"
depends_on:
    - "0440"
spec_refs:
    - spec.md#7.3
---

## Description
Current state: design agreed and accepted on 2026-11-20 (session s_18f059896f075e5a); not started.
The dependency 0440 (write-scoped worktree leases with per-session change attribution) is done.
Client-specific rendering is the separate proposed follow-up 0562 (render cross-session messages in
web, iOS and TUI clients).

### Why
Several sessions can work on one project at once: they share the main worktree (0440) or run in
workstreams. Agents can see each other only indirectly. `list_backlog` appends an "Other live
sessions in this project" footer (`liveSessionLines` in `internal/session/watch.go`), and a task
another live session holds is marked not actionable. Agents cannot coordinate directly, for example
"I'm refactoring internal/tools/worker.go, hold off" or "I need X exported from your package".

### Design
The feature is behind an experimental opt-in, `[experimental] session_messages = true`, in the
project's `.ycc/config.toml` or daemon-wide in `ycc.toml`. This is the first `[experimental]` key.
The tools are registered only when the opt-in is enabled.

- **`list_sessions`** (read-only, coordinator tool). It lists the other top-level live sessions in
  the project's primary tree and its workstreams, reusing `liveSessionLines`. Each row gives the id,
  mode, activity state, focused tasks (held or not held), worktree, running jobs, a one-line session
  title, and a bounded list of paths the session has claimed (from the worktree claims registry).
  The `list_backlog` footer stays.
- **`message_session(session_id, text)`**. The name is distinct from `send_to_agent`, which
  addresses the caller's own subagents. The target must be a top-level session in the same project;
  subagents are reached through their coordinator. Text is capped (about 4 KB), and each
  sender→receiver pair is rate limited. Sending to the caller's own session, an unknown session, or
  one that has ended returns an error, so a message is never silently dropped.
- **Events.** The sender's log records the tool call. The receiver's log records `peer_message`
  (sender session, sender's focused task, text, hop count) when the message is accepted, and
  `peer_message_delivered` referencing that seq when the message enters history. Undelivered
  messages are restored from the log after a reopen or daemon restart.
- **Authority.** A delivered message enters history as synthetic peer context, like a job-completion
  note, never as user input. It is framed as advisory: it cannot authorize commits, grant mutation,
  promote proposed work, or answer the receiver's pending `ask_user`. The coordinator prompt states
  the same rule.
- **Delivery by receiver state:**
  - running: delivered at the next safe checkpoint;
  - blocked in `wait` or awaiting jobs: ends the wait early (as queued user input does), then
    delivered at the checkpoint;
  - idle after a final response: woken to handle it on the session run owner, like
    `resumeForFinishedJobs` (user decision, 2026-11-20, s_18f059896f075e5a);
  - paused, pending question, rollover, refused, blocked, or stopped: queued only, never woken, and
    delivered at the next checkpoint once the boundary clears.
- **Loop limit.** A message sent by a session whose current run was started by a peer message
  carries hop = previous hop + 1. User input resets the chain. Messages above hop 3 queue without
  waking the receiver. The spend guard (spec §20.6) applies to woken runs.
- **Out of scope.** Hard locks or reservations; coordination stays advisory, as with 0440. Soft path
  reservations, the third idea in this task's original draft, are deferred.

## Acceptance criteria
- With the opt-in off, neither tool is offered and no behaviour changes.
- `list_sessions` shows other live sessions in the main tree and workstreams with focus, state, jobs,
  and claimed paths; it omits the caller and sessions of other projects.
- `message_session` delivers to a running receiver at its next checkpoint, ends a receiver's blocking
  `wait`, and wakes an idle receiver; it never wakes a paused, questioning, refused, blocked, or
  stopped session.
- Receiver logs carry durable `peer_message` / `peer_message_delivered` events, and a queued message
  survives reopen; clients without specific rendering still show the message through the generic
  event fallback.
- Delivered messages are framed as peer context and do not count as user input, for example they
  cannot answer `ask_user`.
- Length cap, rate limit, unknown-target errors, and the hop-3 wake limit are covered by tests.
- spec.md §7.3 (or a linked design doc) describes the feature and its experimental opt-in.

## Work log
