---
id: "0411"
title: 'Desktop web: start/resume sessions, session settings, image attachments'
status: todo
priority: 2
created: "2026-10-02"
updated: "2026-10-02"
depends_on:
    - "0410"
spec_refs:
    - docs/design/web-client.md#Desktop-first layout
    - Session input
    - Settings
    - Session history and reopen
---

## Description
Second phase of docs/design/web-client.md. Reference the iOS NewSessionView/NewSessionModel, SessionSettingsView/Model, PictureComposer, and SessionUsageView for parity.

- New-session dialog (button + palette entry): project, mode (ListModes), model/preset, reasoning, an initial prompt with image attachments → StartSession. Navigate to the new session on success.
- Reopen persisted sessions → ResumeSession.
- Session settings panel: SetThinking/reasoning, coordinator model, context-token meter and rollover from SessionViewState, and per-session usage (GetUsage scoped to the session).
- Composer attachments: paste and drag-and-drop images (JPEG/PNG/GIF/WebP within daemon limits), previews before sending, removal. Transcript thumbnails via GetSessionAttachment, opening full-size in the inspector, with the metadata fallback when bytes are unavailable.

## Acceptance criteria
- [ ] Start a session with an image from the browser, reopen a finished one, and change reasoning mid-session. Each change shows up in the transcript and state.
- [ ] Invalid or oversized attachments surface the daemon's error without creating a session.

## Work log
