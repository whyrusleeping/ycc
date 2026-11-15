---
id: "0556"
title: 'iOS: thinking picker beside every model picker'
status: done
priority: 2
created: "2026-11-15"
updated: "2026-11-15"
depends_on:
    - "0554"
spec_refs: []
---

## Description

Status: todo. Blocked by 0554 (daemon: per-model thinking levels and session-only thinking overrides). This task delivers the iOS part of the 2026-11-15 user request: every model picker gets a thinking picker beside it. It is separate from 0554 and from the web task because iOS is verified on the Mac build and simulator.

## Acceptance criteria

- [ ] Each surface above offers a thinking picker next to its model picker, limited to that model's levels.
- [ ] Builds and tests pass on the configured Mac (xcodegen, xcodebuild build/test, `swift test` in YccKit). Simulator screenshots of new session, Start work, the session settings and the watch editor have been checked against the ios-review daemon.

## Outcome

iOS thinking pickers sit beside the model pickers. YccKit's ThinkingLevels helper limits each picker to ModelInfo.thinking_levels (all six levels on older daemons) and clamps a pick to a supported level the way the daemon does (spec §7.4). It is covered by swift tests. New session sends coordinator_thinking through StartSession and SpawnWorkstream. Start work picks a level for each role in that run: the coordinator level goes with the start request, and the implementer and reviewer levels go through session-scoped SetThinking. Roles that share a model stay in sync, and a conflict blocks Start. Session settings shows each role's model with its level, applies changes to that session only and reloads the resolved levels. Global role defaults and review-tier slots offer only supported levels. The watch editor has a thinking picker, and the model editor edits the thinking_levels override. docs/design/ios-client.md is updated. Verified on the Mac: swift test (631 tests), xcodebuild test, and simulator checks against the ios-review daemon (events show coordinator_thinking and session-only thinking_level_changed; ycc.toml unchanged).

Commit: iOS: thinking picker beside every model picker, limited to each model's levels
