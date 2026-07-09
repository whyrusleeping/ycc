---
id: "0181"
title: 'iOS: session list — history, project filter, needs-answer badges'
status: done
priority: 2
created: "2026-07-08"
updated: "2026-07-08"
depends_on:
    - "0180"
spec_refs:
    - docs/design/ios-client.md#6. Screens & feature phases
---

## Description
Session list screen per `docs/design/ios-client.md` §6 phase 1 step 2.

## Description
- List from `ListSessionHistory` (live + persisted, most-recent-first); project filter from `ListProjects` (hidden when ≤1 project); optional `project` param re-query.
- Per row: title, status badge (running/idle/error), live marker, turns, relative last-activity time. `waitingInput:true` rows styled loudest ("needs answer") and sorted/sectioned to the top.
- Pull-to-refresh and refresh on scenePhase → active.
- Navigation: tapping a row pushes the session view (0182's destination; a stub detail is fine until it lands if this task merges first).

## Acceptance criteria
- Rows render all listed fields; protojson quirks handled by the generated client (int64 as strings decoded to Int64).
- Needs-answer sessions are visually unmistakable and listed first.
- Project filter round-trips; empty daemon shows a sane empty state.
- YccKit view-model logic (sorting/sectioning/filtering) covered by `swift test`.

## Plan

Session list screen per docs/design/ios-client.md §6 phase 1 step 2, becoming the authenticated home screen (replacing the placeholder project list in LandingView).

1. YccKit: `YccClient.listSessionHistory(project:)` wrapper (Ycc_V1_ListSessionHistoryRequest, optional project) returning [Ycc_V1_SessionSummary]; keep listProjects.
2. YccKit: `SessionListModel` (@MainActor @Observable) — pure, testable logic:
   - Holds sessions, projects, selected project filter, loading/error state.
   - `sections(from:)` / sorting logic as a pure, unit-testable function: needs-answer sessions (live && waitingInput) in a top "Needs answer" section, remainder most-recent-first by lastActivity (RFC3339 parse; fall back to startedAt/stable order on parse failure).
   - refresh() re-queries ListSessionHistory with the selected project ("" = default/all per RPC semantics) and ListProjects; unauthorized bubbles to AppModel.
3. App: rewrite `LandingView` (or new `SessionListView` that LandingView hosts) as the home screen:
   - NavigationStack; per-row: title (fallback to mode/session id when empty), status badge (running/idle/error/paused/stopped colored), live marker, waiting-input "Needs answer" styling (loudest: tinted background + bell icon), turns count, relative last-activity time (RelativeDateTimeFormatter / Date.RelativeFormatStyle).
   - Project filter as a toolbar Menu (chips ok) fed by ListProjects; hidden when ≤1 project; changing it re-queries.
   - Pull-to-refresh (.refreshable), refresh on scenePhase → .active, sane empty state (ContentUnavailableView), Disconnect stays in toolbar.
   - Row tap pushes SessionView(client:project:sessionID:live:) — live rows stream, non-live render the persisted transcript.
4. Tests (headless swift test): sorting/sectioning (waitingInput first, recency order, ties/missing timestamps), title fallback, relative-time/status-badge mapping logic if extracted, project-filter round-trip via a mock source (record requested project).
5. Verify: cd clients/ios/YccKit && swift test; cd clients/ios && xcodegen generate && xcodebuild -destination 'generic/platform=iOS Simulator' build. protojson int64 (turns) is handled by generated code — just render Int64.

### Starting points
- clients/ios/App/LandingView.swift — current placeholder project list; replace with the session list as the authenticated home
- clients/ios/App/SessionView.swift — destination from 0182: init(client:project:sessionID:live:)
- clients/ios/YccKit/Sources/YccKit/YccClient.swift — add listSessionHistory(project:); listProjects already exists
- proto/ycc/v1/ycc.proto SessionSummary — sessionId, mode, status(running|idle|error|paused|stopped), workspace, title, startedAt, lastActivity, focusTasks, turns, toolCalls, live, waitingInput
- clients/ios/YccKit/Tests/YccKitTests/ — existing headless test patterns (SessionViewModelTests mock-source style)
- verify: cd clients/ios/YccKit && swift test; cd clients/ios && xcodegen generate && xcodebuild -project Ycc.xcodeproj -scheme Ycc -destination 'generic/platform=iOS Simulator' build

## Work log
- 2026-07-08 plan: Session list screen per docs/design/ios-client.md §6 phase 1 step 2, becoming the authenticated home screen (replacing the placeholder project list in LandingView).  1. YccKit: `YccClient.listSession
…[truncated]
- 2026-07-08 context hints: 6 recorded with plan
- 2026-07-08 context hints: clients/ios/App/LandingView.swift — current placeholder project list; replace with the session list as the authenticated home; clients/ios/App/SessionView.swift — destination from 0182: init(clien
…[truncated]
- 2026-07-08 implementer report: Implemented the iOS session-list home screen (task 0181, docs/design/ios-client.md §6 phase 1 step 2).  ## Changes - **YccKit/YccClient.swift**: added `listSessionHistory(project:)` wrapping the gene
…[truncated]
- 2026-07-08 review tier: single-opus — reviewers: claude
- 2026-07-08 review (claude): accept — The change implements the iOS session-list home screen per the task. YccClient.listSessionHistory(project:) wraps the generated RPC following existing patterns; SessionListModel provides pure, well-te
…[truncated]
- 2026-07-08 decision: accept — commit: iOS: session list home — history, project filter, needs-answer badges (task 0181)
- 2026-07-08 usage: 24,088 tok (in 82, out 24,006, cache_r 1,622,626, cache_w 83,850) · $2.5171
  implementer: 16,317 tok (in 42, out 16,275, cache_r 672,902, cache_w 43,261) · $1.0139
  coordinator: 4,288 tok (in 20, out 4,268, cache_r 786,708, cache_w 12,956) · $1.1623
  reviewer:claude: 3,483 tok (in 20, out 3,463, cache_r 163,016, cache_w 27,633) · $0.3409
