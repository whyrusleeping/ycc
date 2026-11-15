---
id: "0556"
title: 'iOS: thinking picker beside every model picker'
status: todo
priority: 2
created: "2026-11-15"
updated: "2026-11-15"
depends_on:
    - "0554"
spec_refs: []
---

## Description
Status: todo. Blocked by 0554 (daemon: per-model thinking levels and session-only thinking overrides). This task delivers the iOS part of the 2026-11-15 user request: every model picker gets a thinking picker beside it. It is separate from 0554 and from the web task because iOS is verified on the Mac build and simulator.

## Work
- YccKit: a shared helper that limits a picker to a model's `thinking_levels` and moves to the nearest supported level when the model changes. Cover it in `swift test`.
- New session (`NewSessionView` / `NewSessionModel`): coordinator thinking next to the model, sent through StartSession and SpawnWorkstream.
- Start work options (`StartWorkOptionsView` / `StartWorkOptionsModel`): thinking per role model for that run, applied session-only.
- Session settings (`SessionSettingsView` / `SessionSettingsModel`): reasoning next to each role model instead of a separate scope picker. The copy says the change applies to this session only.
- Global settings roles and the review tier slots: limit the existing pickers to supported levels.
- Watch editor (`WatchEditorView`): thinking next to model.
- Model editor: edit the `thinking_levels` override.
- Update docs/design/ios-client.md.

## Acceptance criteria
- [ ] Each surface above offers a thinking picker next to its model picker, limited to that model's levels.
- [ ] Builds and tests pass on the configured Mac (xcodegen, xcodebuild build/test, `swift test` in YccKit). Simulator screenshots of new session, Start work, the session settings and the watch editor have been checked against the ios-review daemon.

## Work log
