---
id: "0016"
title: Quick-add backlog items mid-session (TUI capture overlay)
status: todo
priority: 3
created: "2026-06-26"
updated: "2026-06-26"
depends_on:
    - "0006"
spec_refs:
    - Client UI (TUI)
    - Modes (the home menu)
    - Document model
---

## Description
While a session is running (e.g. a long `work` run), I want to capture a new backlog item
without interrupting the agent — a pop-up overlay (sibling to the settings overlay §18.2)
where I describe the item by *talking to a lightweight agent* that turns it into a
structured task via `create_task`. The main session keeps running and is undisturbed.

Design notes:
- TUI overlay on its own keybinding (distinct from esc/settings). Small input; submit to a
  capture agent; show the created task id (and any clarifying question it asks).
- "Talk to the agent": route the text to a short-lived capture agent scoped to the same
  project — effectively `backlog` mode trimmed to Read + list_backlog + get_task +
  create_task + ask_user. It may ask ONE clarifying question, then create the task.
- Keep it off the main session's event stream: a separate transient session/stream (or a
  dedicated `CaptureBacklogItem` RPC that runs the capture agent and returns the task id).
- Concurrency: the capture writes `backlog/` files + regenerates `backlog.md` while a
  `work` session may also be mutating the backlog — serialize backlog writes in
  `docs.Store` (mutex) so the index can't be regenerated from a half-written state.

## Acceptance criteria
- [ ] a TUI overlay (own keybinding) opens over a running session without pausing it
- [ ] the user describes an item in natural language; a lightweight agent turns it into a
      structured task via `create_task`, optionally asking one clarifying question first
- [ ] the new task appears in the backlog (task file + regenerated index) and the main
      session is unaffected (its stream/turn continues)
- [ ] backlog writes are concurrency-safe when a `work` session runs simultaneously
- [ ] works in one-shot (single project) and attached/multi-project modes (scopes to the
      current project)

## Work log
