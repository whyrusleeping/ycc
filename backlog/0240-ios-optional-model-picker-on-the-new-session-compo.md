---
id: "0240"
title: 'iOS: optional model picker on the new-session composer (per-session coordinator override)'
status: in_review
priority: 3
created: "2026-08-05"
updated: "2026-08-05"
depends_on: []
spec_refs:
    - docs/design/ios-client.md#Phase 2 — start work, backlog
    - docs/remote-api.md#StartSession
---

## Description
Let a new chat session started from the phone optionally run on a chosen logical
model instead of the configured coordinator.

- `StartSessionRequest.coordinator_model` (new field 6): names a logical model
  from `ListModels` for the session's coordinator. Per-session only — the
  persisted role defaults in `ycc.toml` and the implementer/reviewer roles are
  untouched. Unknown name => `invalid_argument`, rejected before any session
  state/log is created (`session.ErrUnknownModel`).
- `session_started` records the coordinator model and `event.Projection` folds it
  (also from `role_config_changed`), so `ResumeSession` replays a session on the
  model it ran on, falling back to the default if that model was removed.
- iOS: `NewSessionModel` loads `ListModels`, exposes `models` / `defaultModel` /
  `selectedModel` / `showsModelPicker`, and passes the override to
  `StartSession`. `NewSessionView` gains a third chip (shown only when >1 model is
  configured) with a "Default" entry. The pick is NOT remembered across sessions.
  A `ListModels` failure hides the chip instead of blocking the composer.

## Acceptance criteria

- [x] proto + regenerated Go/Swift code
- [x] daemon applies and validates the override; defaults unchanged
- [x] resume replays on the session's model
- [x] Go tests (session override/reject, reduce projection) pass
- [x] Swift unit tests for the picker added
- [ ] `swift test` + Xcode build verified on the Mac (no Swift toolchain on the workspace machine)
- [x] spec.md §12 + docs/remote-api.md + docs/design/ios-client.md updated


## Work log
