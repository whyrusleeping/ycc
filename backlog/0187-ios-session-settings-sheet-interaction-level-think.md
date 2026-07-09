---
id: "0187"
title: 'iOS: session settings sheet — interaction level, thinking, role/model config'
status: done
priority: 4
created: "2026-07-08"
updated: "2026-07-08"
depends_on:
    - "0182"
spec_refs:
    - "18.2"
    - docs/design/ios-client.md#6. Screens & feature phases
---

## Description
Phone analog of the TUI settings overlay (spec §18.2) per `docs/design/ios-client.md` §6 phase 3 step 8.

## Description
- A per-session settings sheet on the session view: interaction level (`SetInteractionLevel`), thinking/effort (`SetThinking`), per-role model bindings (`SetRoleConfig`, populated from `ListModels`).
- Read current values where the API exposes them (e.g. GetModelConfig / session events) so the sheet reflects reality, not defaults.
- Changes apply to the live session and are reflected in subsequent events.

## Acceptance criteria
- Each setting round-trips against a live daemon and visibly affects the session (level change event, model change on next turn).
- Invalid combinations surface the daemon's error cleanly.
- View-model logic under `swift test`.

## Plan

Per-session settings sheet for the iOS session view (spec §18.2 analog; docs/design/ios-client.md §6 phase 3 step 8).

1. YccKit:
   - YccClient wrappers: `setInteractionLevel(sessionId:level:)`, `setRoleConfig(sessionId:coordinator:implementer:reviewers:)` (empty = unchanged), `setThinking(sessionId:level:role:)` (role empty = all; levels off|low|medium|high|xhigh|max), `listModels()` → (models, current coordinator/implementer/reviewers assignments, per-role thinking levels).
   - `SessionSettingsModel` (@MainActor @Observable, injectable source protocol): load() seeds pickers from ListModels (role assignments + thinking) and the session's current interaction level (passed in from the session view — the projection/summary knows it; verify what SessionViewModel exposes and thread it through); apply methods per setting that call the RPC, track in-flight state, surface daemon errors verbatim, and update local state on success. Reuse the existing InteractionLevel enum.
   - Tests: seeding from ListModels, each setter's request mapping (incl. empty-field "unchanged" semantics and reviewers list), error surfacing, unauthorized.
2. App:
   - SettingsSheet on SessionView (gear toolbar icon): sections for Interaction level (picker → SetInteractionLevel), Thinking (per-role or all-roles level picker → SetThinking), Roles (coordinator/implementer pickers + reviewers multi-select from ListModels models → SetRoleConfig). Apply on change with progress indication; errors as inline rows/alerts; unauthorized routes to connect.
   - Level changes should be visible in the live feed (interaction_level_changed event renders as a system row — verify the projection handles it; if it already does, nothing more needed).
3. Verify: swift test; xcodegen + xcodebuild simulator build; extend plans/ios-client-smoke.md with settings-sheet smoke steps.

### Starting points
- proto/ycc/v1/ycc.proto lines ~195-310 — ListModelsResponse carries CURRENT role assignments + per-role thinking (seed pickers from it); SetInteractionLevel/SetRoleConfig/SetThinking request shapes (empty fields = leave unchanged)
- clients/ios/App/SessionView.swift — session toolbar to hang the settings sheet off; SessionViewModel/SessionProjection may expose current interaction level
- clients/ios/YccKit/Sources/YccKit/NewSessionModel.swift — InteractionLevel enum already exists; reuse
- clients/ios/YccKit/Sources/YccKit/UsageModel.swift — latest source-protocol + tests pattern
- internal/tui settings overlay (rg 'SetRoleConfig|SetThinking' internal/tui) — semantics reference
- plans/ios-client-smoke.md — extend

## Work log
- 2026-07-08 plan: Per-session settings sheet for the iOS session view (spec §18.2 analog; docs/design/ios-client.md §6 phase 3 step 8).  1. YccKit:    - YccClient wrappers: `setInteractionLevel(sessionId:level:)`, `s
…[truncated]
- 2026-07-08 context hints: 6 recorded with plan
- 2026-07-08 context hints: proto/ycc/v1/ycc.proto lines ~195-310 — ListModelsResponse carries CURRENT role assignments + per-role thinking; SetInteractionLevel/SetRoleConfig/SetThinking shapes (empty fields = leave unchanged)
…[truncated]
- 2026-07-08 implementer report: Implemented task 0187 — the iOS per-session settings sheet (interaction level, thinking, role/model config), the phone analog of the TUI settings overlay (spec §18.2).  ## YccKit (headless logic + 
…[truncated]
- 2026-07-08 review tier: single-opus — reviewers: claude
- 2026-07-08 review (claude): accept — The change delivers the per-session settings sheet cleanly and completely. YccClient gains correctly-shaped wrappers for ListModels/SetInteractionLevel/SetRoleConfig/SetThinking; SessionSettingsModel 
…[truncated]
- 2026-07-08 decision: accept — commit: iOS: session settings sheet — interaction level, per-role thinking, role/model config (task 0187)
- 2026-07-08 usage: 41,246 tok (in 162, out 41,084, cache_r 5,093,452, cache_w 307,948) · $7.0160
  implementer: 28,761 tok (in 90, out 28,671, cache_r 3,088,498, cache_w 166,009) · $3.2990
  reviewer:claude: 6,577 tok (in 36, out 6,541, cache_r 463,287, cache_w 46,119) · $0.6836
  coordinator: 5,908 tok (in 36, out 5,872, cache_r 1,541,667, cache_w 95,820) · $3.0334
