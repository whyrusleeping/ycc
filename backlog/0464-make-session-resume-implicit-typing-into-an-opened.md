---
id: "0464"
title: 'Make session resume implicit: typing into an opened session auto-resumes it'
status: done
priority: 3
created: "2026-10-14"
updated: "2026-10-15"
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

## Outcome

Resuming is now a backend detail. Server: SendInput uses Manager.ReopenForInput, which finds the session's project by id and reopens it (NotFound only when there is no persisted log). Input is validated before the reopen. TUI: after an accepted send it reattaches the stream from NotFound, lost, auth, terminal or stream-closed states, and a latched stopped/stream-closed status clears on activity. Web and iOS: a send while paused is followed automatically by Resume. Resume labels and instructions now say Continue, and the stop dialogs say a later message continues the session. ResumeSession is kept as an idempotent eager-attach RPC. Spec §18.6/§18.7, remote-api, README and the smoke plans are updated, and the web bundle is rebuilt. Tests: Go server test (idle-after-restart and stopped sessions continue), a TUI reattach test (3 states), a web paused-send test, and an updated iOS VM test.

Commit: Make session resume implicit: sending reopens and continues sessions
