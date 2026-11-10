---
id: "0522"
title: 'iOS: new session defaults to chat; onboarding-only PM entry; relocate maintenance presets'
status: todo
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
