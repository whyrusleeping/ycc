---
id: "0464"
title: 'Make session resume implicit: typing into an opened session auto-resumes it'
status: todo
priority: 3
created: "2026-10-14"
updated: "2026-10-14"
depends_on: []
spec_refs: []
---

## Description
"Resuming" a session is currently a separate, user-facing step. It shouldn't be. Resuming is a backend detail. When the user opens an existing session and starts typing, it should resume without any extra step.

Scope: session tooling and UI (sessions, `.ycc/sessions/`, `session-view.sqlite`, TUI/web/iOS clients).

Moved from the valstore backlog (valstore 0410, filed there 2026-10-09 by mistake) at the owner's request on 2026-10-14.

## Acceptance criteria
- Opening an existing session and sending a message resumes it automatically. There is no separate "resume" action, button, command or prompt.
- No user-facing text, buttons or commands mention "resume" or "resuming" as a step the user has to take. Status messages may still mention it if that helps.
- When the backend can't resume a session (missing state, process gone, etc.), it handles that without the user's involvement: it reattaches or restarts using the saved context and shows a clear message only if that fails.
- Any existing explicit resume entry point is removed, or kept only as an internal/back-compat alias.
- Tests cover: open an idle or stopped session, type, and the message is delivered and the session continues, with no manual resume.

## Work log
