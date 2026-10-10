---
id: "0441"
title: Let users flag a session as "come back to this" (follow-up marker) across clients
status: todo
priority: 3
created: "2026-10-09"
updated: "2026-10-09"
depends_on: []
spec_refs: []
---

## Description
## Problem
I often read an agent's reply, need time to think, and then forget to go back to that session. Right now I can't mark a session as "needs my attention later," so it gets lost among the other idle sessions.

## Idea (design open)
Add a per-session follow-up flag that the user sets themselves. The daemon stores it, and every client (TUI, iOS, desktop web) can see it. Questions to settle:
- **Storage/RPC:** where the flag lives (session metadata on the daemon) and an RPC to set or clear it. It could be a simple boolean, or an optional short note ("check X") plus an optional remind-at time.
- **Surfacing:** a badge or icon on flagged sessions, a "Follow up" filter or section at the top of session lists, and maybe a count on the home/workspace hub.
- **Clearing:** manual clear, and/or auto-clear when the user sends the next message in that session (decide which is the default).
- **Reminders (optional, later):** a snooze or remind-at time that resurfaces the session, e.g. via an iOS notification.
- Keep this separate from agent-driven status ("awaiting input", "idle"). This flag is the user's own bookmark.

## Acceptance criteria
- The user can flag and unflag a session from the TUI, iOS and desktop web. The flag survives daemon restarts.
- Flagged sessions are clearly marked and can be listed or filtered in each client.
- The clearing behavior (manual vs. auto on reply) is decided and documented.
- Optional note and remind-at are either built or explicitly deferred to a follow-up task.
- Tests cover setting, clearing and persisting the flag.

## Work log
