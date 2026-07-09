---
id: "0185"
title: 'iOS: start & resume sessions — mode/level pickers, prompt composer'
status: done
priority: 3
created: "2026-07-08"
updated: "2026-07-08"
depends_on:
    - "0181"
    - "0182"
spec_refs:
    - 9. Modes (the home menu)
    - docs/design/ios-client.md#6. Screens & feature phases
---

## Description
Start and resume sessions from the phone per `docs/design/ios-client.md` §6 phase 2 step 5.

## Description
- "New session" flow: mode picker from `ListModes` (pm/chat/work + preset descriptions), interaction level picker (interactive/judgement/autonomous), project picker (registered projects), multiline prompt composer → `StartSession`, then navigate directly into the live session view (Subscribe from seq 0).
- Resume: `ResumeSession` action on persisted session rows (re-opens on the existing log, idempotent if live); navigate into the live view on success.
- Sensible defaults (last-used mode/level/project remembered client-side).

## Acceptance criteria
- Starting a work/pm/chat session from the app lands in a live streaming view of that session.
- Resuming a persisted session continues the same event log (seq continuity visible).
- Errors (unknown project, daemon unreachable) surfaced cleanly.
- View-model logic under `swift test`.

## Plan

Add "start a new session" and "resume persisted session" flows to the iOS app.

1. YccKit (headless-testable logic):
   - Extend YccClient with typed wrappers: `listModes()` (ListModes → modes with name/description), `startSession(project:mode:prompt:interactionLevel:)` → returns session id (map errors to YccError), `resumeSession(project:sessionId:)` → returns session id (idempotent when live per remote-api).
   - New `NewSessionModel` (@Observable, @MainActor like SessionListModel): loads modes + projects, holds selections (mode, interaction level enum interactive/judgement/autonomous, project, prompt text), validation (non-empty prompt where required), `start()` async → session id or errorMessage; remembers last-used mode/level/project via a small injectable defaults store (protocol over UserDefaults so tests can stub it).
   - Unit tests for NewSessionModel: defaults persistence/recall, validation, error surfacing, successful start path (stub client source protocol, following the existing SessionListSource pattern in SessionListModel).
2. App (SwiftUI shell):
   - LandingView toolbar "+" button → sheet with NewSessionView: mode picker (with preset descriptions), interaction level picker, project picker, multiline prompt TextField, Start button with progress state; on success dismiss and push SessionView(live: true, fromSeq 0).
   - Resume: on persisted session rows add a swipe action / detail affordance calling resumeSession, then navigate into the live view (SessionView live: true). Simplest robust wiring: navigate by session id after resume returns.
   - Errors (unknown project, unreachable daemon) shown inline in the sheet / as alert.
3. Verify: `cd clients/ios/YccKit && swift test`; `cd clients/ios && xcodegen generate && xcodebuild -project Ycc.xcodeproj -scheme Ycc -destination 'generic/platform=iOS Simulator' build`. Update plans/ios-client-smoke.md with a start/resume smoke step.

Check proto shapes first (StartSessionRequest fields: project, mode, prompt, interaction level naming) rather than guessing.

### Starting points
- clients/ios/YccKit/Sources/YccKit/YccClient.swift — wrapper style + error mapping to follow
- clients/ios/YccKit/Sources/YccKit/SessionListModel.swift — @Observable model + injectable source-protocol test pattern
- clients/ios/App/LandingView.swift — toolbar + NavigationStack to hook the sheet/navigation into
- clients/ios/App/SessionView.swift — live session view (init signature: client/project/sessionID/live)
- proto/ycc/v1/ycc.proto — StartSessionRequest/ResumeSessionRequest/ListModesResponse field names
- docs/remote-api.md §StartSession/§ResumeSession — wire semantics, idempotent resume
- Build/test: cd clients/ios/YccKit && swift test; cd clients/ios && xcodegen generate && xcodebuild -project Ycc.xcodeproj -scheme Ycc -destination 'generic/platform=iOS Simulator' build (keep ad-hoc signing)

## Work log
- 2026-07-08 plan: Add "start a new session" and "resume persisted session" flows to the iOS app.  1. YccKit (headless-testable logic):    - Extend YccClient with typed wrappers: `listModes()` (ListModes → modes with 
…[truncated]
- 2026-07-08 context hints: 7 recorded with plan
- 2026-07-08 context hints: clients/ios/YccKit/Sources/YccKit/YccClient.swift — wrapper + error-mapping style to extend; clients/ios/YccKit/Sources/YccKit/SessionListModel.swift — @Observable model + injectable source-protoc
…[truncated]
- 2026-07-08 implementer report: Implemented iOS start & resume session flows (task 0185).  ## YccKit (headless logic) - **YccClient.swift**: added typed wrappers `listModes()` → `(modes, presets)`, `startSession(project:mode:promp
…[truncated]
- 2026-07-08 review tier: single-opus — reviewers: claude
- 2026-07-08 review (claude): accept — The change fully implements the iOS start & resume session flows per task 0185. YccClient gains typed listModes/startSession/resumeSession wrappers with correct proto field mapping and error translati
…[truncated]
- 2026-07-08 decision: accept — commit: iOS: start & resume sessions — mode/level/project pickers, prompt composer, resume affordance (task 0185)
- 2026-07-08 usage: 29,951 tok (in 100, out 29,851, cache_r 2,085,639, cache_w 151,744) · $3.1524
  implementer: 23,880 tok (in 60, out 23,820, cache_r 1,578,786, cache_w 74,451) · $1.8505
  reviewer:claude: 4,102 tok (in 30, out 4,072, cache_r 342,625, cache_w 31,975) · $0.4731
  coordinator: 1,969 tok (in 10, out 1,959, cache_r 164,228, cache_w 45,318) · $0.8288
