---
id: "0241"
title: ListModels should report a live session's role assignment, not just the defaults
status: proposed
priority: 3
created: "2026-08-05"
updated: "2026-08-05"
depends_on: []
spec_refs:
    - 18.2 Settings overlay (esc — "video-game style")
---

## Description
`ListModels` reports the daemon's persisted default role assignments only. Now
that a session can be started with a per-session coordinator override
(`StartSession.coordinator_model`, task 0240), the session settings sheet (iOS)
and TUI overlay seed their pickers from the GLOBAL defaults even when the live
session runs on a different model — it misreports reality, and applying a change
silently rewrites the persisted defaults.

Idea: `ListModelsRequest` gains an optional `session_id`; when it names a live
session the response's `coordinator`/`implementer`/`reviewers` + thinking levels
report THAT session's live assignment (`Session.coordinator` etc.) instead of the
config defaults. Clients pass the session id from the session settings sheet and
leave it empty for the global Settings screen.

Open question: should `SetRoleConfig` with a session id also stop persisting the
change as the new default (today it does both), so a per-session change stays
per-session? That would make start-time and mid-session overrides consistent.

## Acceptance criteria

- [ ] `ListModels` reflects a live session's actual role assignment when given its id
- [ ] iOS session settings sheet + TUI overlay pass the session id
- [ ] decided + documented whether an in-session `SetRoleConfig` still writes the default


## Work log
