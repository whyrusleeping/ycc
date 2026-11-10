---
id: "0522"
title: 'iOS: new session defaults to chat; onboarding-only PM entry; relocate maintenance presets'
status: in_review
priority: 2
created: "2026-11-10"
updated: "2026-11-10"
depends_on:
    - "0521"
spec_refs:
    - spec.md#9. Modes
    - docs/design/ios-client.md
---

## Description
iOS counterpart of 0521. Today `App/NewSessionView.swift` shows a mode chip (picker over every mode) and preset suggestion cards in the empty transcript area (`NewSessionModel.suggestedPresets`).

**Scope**
- New session starts chat. Remove the mode chip from the default chip row, or move it behind an advanced/long-press affordance consistent with 0521. Starting from a task (backlog task detail) still opens work for that task.
- Suggestion cards show only "Onboard this project" when the selected project needs onboarding, using 0521's ListModes placement metadata rather than hardcoded names.
- Spec-doctor and re-onboarding ("plan the backlog with PM") are reachable from the project or backlog context, matching web. Memory-groom stays in MemoryView.

**Acceptance**
- Built and tested on the configured Mac (xcodegen, xcodebuild test, `swift test` in YccKit).
- Simulator screenshots of new session for an onboarded and an un-onboarded project, plus the relocated actions.
- Task-started sessions still use work mode.
- Status `in_review` until the owner has used it on device.

## Acceptance criteria

## Work log

- 2026-11-10: Implemented. `YccClient.listModes` returns the whole `ListModesResponse`; `NewSessionModel` starts in `default_mode` (last-used mode no longer remembered; project still is), `suggestedPresets` is only onboarding-placed presets for a project needing onboarding, `isDefaultMode` drives the chip row (mode chip leads only for a non-default mode, else a trailing icon-only "Advanced: mode" chip). New `PresetPlacement` + `PresetActionsModel` (YccKit) and `presetActionButtons` (App): project overflow gets onboarding/Re-onboard + "Check docs against code"; backlog's sort menu became a ⋯ menu with Sort, "Groom the backlog", onboarding/Re-onboard. TaskDetail start (work) and MemoryView groom unchanged. Mac: `swift test` 587 passed; `xcodebuild test` (iPhone 18 Pro Max sim) succeeded. Sim checks against the sidebar-v2 harness (`fresh` un-onboarded, `ycc-demo` onboarded): screenshots in `.ycc/tmp/i0522/shots` (light/dark new session both kinds, advanced menu, onboarding applied, both project menus, backlog menus); "Check docs against code" started a pm spec-doctor session, a plain prompt started chat.
