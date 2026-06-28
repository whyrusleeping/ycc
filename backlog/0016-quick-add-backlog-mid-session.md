---
id: "0016"
title: Quick-add backlog items mid-session (TUI capture overlay)
status: done
priority: 3
created: "2026-06-26"
updated: "2026-06-27"
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
- 2026-06-27 plan: Add a "quick-add backlog item" capture overlay that runs a lightweight capture agent server-side without disturbing the running session, plus concurrency-safe backlog writes.  1) docs.Store concurrenc
…[truncated]
- 2026-06-27 implementer report: Implemented task 0016 (quick-add backlog items mid-session) end-to-end.  1) docs.Store concurrency-safe writes (internal/docs/docs.go) - Added a package-level registry of per-directory `*sync.Mutex` (
…[truncated]
- 2026-06-27 review (claude): accept — The change fully implements task 0016. A new CaptureBacklogItem RPC runs a lightweight, off-stream capture agent (RunCapture in internal/orchestrator/capture.go) scoped to a project, using a trimmed r
…[truncated]
- 2026-06-27 decision: accept — commit ee89417: Quick-add backlog items mid-session via TUI capture overlay (§18.2, task 0016)  Add a ctrl+n capture overlay that turns a natural-language description into a structured backlog task without disturbin
…[truncated]
- 2026-06-27 usage: 64,246 tok (in 266, out 63,980, cache_r 10,011,071, cache_w 388,173) · cost n/a (unpriced)
