---
id: "0443"
title: 'Experimental session awareness: list_sessions, path ownership lookup, and same-file overlap notices'
status: done
priority: 3
created: "2026-10-09"
updated: "2026-11-20"
depends_on:
    - "0440"
spec_refs:
    - spec.md#7.3
---

## Description

Current state: redesigned and accepted on 2026-11-20 (session s_18f059896f075e5a); not started. The
dependency 0440 (write-scoped worktree leases with per-session change attribution) is done. This
task delivers the awareness part of cross-session coordination. Related tasks:
- 0564 adds passive coordination notices (a notice board) on top of this task's setting and
  discovery tool.
- 0562 adds client rendering and the iOS toggle.
- 0565 handles the larger commit sweep-in problem.

An earlier version of this task planned a directed `message_session` tool. The user dropped it on
2026-11-20 (s_18f059896f075e5a) after two independent reviews of the wheeler project's session logs
from 2026-11-15 to 2026-11-20. Those logs showed that agents mostly needed one-to-many,
non-waking announcements and ownership facts, not conversations.

### Evidence (wheeler, 2026-11-15 to 2026-11-20, about 179 sessions in one shared tree)
- About 92 sessions saw the `list_backlog` "Other live sessions" footer, and no task was worked by
  two sessions at once. Knowing who was live was not the gap. Knowing what each was doing was: 76 of
  140 non-watch sessions were chat sessions with no task focus, so the footer gave no purpose for them.
- Agents repeatedly needed to know whose uncommitted change a dirty file held, often from a session
  that had already ended.
- On 2026-11-16 (07:35–08:08), the sessions for 0302 and 0287 both edited
  `cmd/wheeler/normalizev2.go`. Each guessed what the other would do. The review snapshot included
  the other's hunks and failed to compile for three rounds, and the episode ended in an `ask_user`
  and a commit built with git plumbing. A notice to both writers at the moment of overlap needs
  neither agent to start a conversation.

### Design
- **Setting.** A per-project experimental setting, `[experimental] session_coordination` in the
  project's `.ycc/config.toml`, default off, with no daemon-wide switch (user decision, 2026-11-20,
  s_18f059896f075e5a). Wheeler sessions read scraped web content and hold production SSH access, so
  enabling the feature for one project must not enable it for another. The web Settings page gets a
  per-project section showing and changing it through a new RPC; no per-project settings editor
  exists yet (`auto_groom` is display-only). The iOS toggle is in 0562.
  - Enabling applies to new and reopened sessions, because a session's tool set is fixed when its
    loop is built; the toggle says so.
  - Disabling applies immediately: tools in running sessions return a disabled result and no further
    notices are delivered.
- **`list_sessions`** (read-only). Coordinators and mutating subagents (implementers and mutating
  generic agents) get it; read-only subagents do not.
  - Without arguments it lists the other top-level sessions of the project's primary tree and its
    workstreams, reusing `liveSessionLines`. Each row gives the id, mode, activity state, held
    focus versus earlier focus, a one-line title (from the opening prompt when there is no task
    focus), worktree, running jobs, and claimed paths with claim age, bounded and marked when
    truncated.
  - `path=` returns that path's claimants from the worktree claims registry, including sessions that
    have ended, with live or ended state, last activity, and focused task.
  - Claims are evidence of past writes, not reservations; the tool description says so.
- **Overlap notices.** When a write claims a code path that another session's scope already claims,
  both sides learn of it once for each path and session pair:
  - the writer, in the Edit/Write result (or, for shell-attributed claims, the Bash result), so a
    writing subagent sees it directly;
  - the other session's coordinator, if live, as a notice at its next safe checkpoint. It is never
    woken: no idle wake and no early end of `wait` (user decision, 2026-11-20).
  Backlog task files and `memory.md` are never claimed, so they never trigger notices. The notice
  names the path, the other session, and its focused task or title.
- **Delivery and replay.** Notices delivered to another session enter history as synthetic context
  with their own actor, not `user`, and are recorded as a durable event at delivery. Notices pending
  delivery are not restored after a reopen or daemon restart; the overlap can be rediscovered with
  `list_sessions`. Every switch that enumerates history-entering events handles the new event,
  including `internal/engine/replay.go`, `internal/session/history.go`, and
  `internal/session/context_rollover.go`. Clients without specific rendering show it through the
  generic event fallback.
- **Out of scope.** Directed agent-to-agent messages, hard locks, and reservations. Coordination
  stays advisory, as with 0440.

## Acceptance criteria

- The per-project setting is off by default, persists in `.ycc/config.toml`, and can be changed from
  the web Settings page; enabling it for one project does not affect another.
- With the setting off, no tool or notice is added and behaviour is unchanged. Turning it off stops
  further notices and makes the tools in running sessions return a disabled result.
- `list_sessions` shows the fields above for other sessions in the main tree and workstreams, omits
  the caller and other projects, and `path=` reports claimants including ended sessions.
- A write that overlaps another scope's claim on a code path produces one notice in the writer's tool
  result and one checkpoint notice for a live other coordinator, without waking it or ending its
  `wait`. Repeated writes to the same path by the same pair do not repeat the notice.
- Notice delivery is recorded durably and replays identically; notices pending delivery are not
  restored after reopen.
- spec.md §7.3 (or a linked design doc) describes the feature and its per-project experimental
  setting.

## Outcome

Per-project [experimental] session_coordination in .ycc/config.toml (default off; syntax-aware single-value edit, refuses forms it cannot locate) exposed as ProjectInfo.session_coordination + SetProjectSettings RPC and a per-project Experimental section in web Settings. Sessions built with it on get list_sessions (coordinators, implementers, mutating generic agents): other sessions with title, focus, jobs and claimed paths with claim age (claim times persisted), or path= claimants incl. ended sessions, scoped to the project (symlink-resolved; other projects never named). Overlapping writes notify once per path/pair: writer in Write/Edit/Bash result, other live coordinator at its next checkpoint via a system-actor coordination_notice (never woken; replay/history/rollover handle it; pending notices memory-only, dropped when off). Spec §7.3, remote-api.md. Review: opus accepted; sol's last finding (exotic TOML such as unicode-escaped keys) fails safe with an error and was judged non-blocking.

Commit: session coordination (experimental, per project): list_sessions, path ownership lookup, same-file overlap notices
