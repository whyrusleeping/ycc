---
id: "0260"
title: 'iOS session view: show which model is doing the work'
status: in_review
priority: 3
created: "2026-08-06"
updated: "2026-08-06"
depends_on: []
spec_refs:
    - docs/design/ios-client.md#Phase 1 — connect, read, interact
---

## Description
The iOS session screen never said which logical model was producing the
session's turns — unlike the TUI status bar, which shows the coordinator model
beside its thinking level. `ListModels` cannot answer this: it reports only the
daemon's GLOBAL role defaults, so a session started with a per-session
`StartSession.coordinator_model` override (or changed mid-session) would be
misreported.

Implementation: fold the model from the event log in `SessionProjection`
(`coordinatorModel`), sourced from `session_started.coordinator`,
`role_config_changed.coordinator` and each coordinator `model_turn`'s
`model_name` (authoritative — the model that actually ran the turn). Subagent
(implementer/reviewer) turns never move it.

Surfaces:
- toolbar subtitle: `project · model · live`
- streaming tail caption: `<model> · streaming`
- pre-activity progress row: `<model> is working…`
- `session_started` system row: `Session started · work · <model>`
- session settings sheet seeds its coordinator picker from the session's folded
  model instead of the global default (which previously could silently reassign
  the session's model on the next apply).

## Acceptance criteria

- [x] `SessionProjection.coordinatorModel` folds from the three event sources,
      ignores subagent turns, and never regresses to empty on older/unparsable
      payloads.
- [x] Session chrome (title subtitle, live tail, working row, started row)
      names the model when known and degrades silently when not.
- [x] `SessionSettingsModel` accepts a `sessionCoordinator` seed, preferring it
      over the `ListModels` default when it is a configured model.
- [x] YccKit unit tests cover the fold and the settings seed.
- [ ] `swift test` + Xcode build verified on the Mac (no Swift toolchain here).

## Work log
