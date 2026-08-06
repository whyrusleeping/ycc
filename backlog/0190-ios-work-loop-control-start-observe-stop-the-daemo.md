---
id: "0190"
title: 'iOS: work-loop control — start/observe/stop the daemon-side loop, digest view'
status: in_review
priority: 4
created: "2026-07-08"
updated: "2026-08-06"
depends_on:
    - "0179"
    - "0183"
spec_refs:
    - 9. Modes (the home menu)
    - docs/design/ios-client.md#9. Daemon-side work loop (decision, prerequisite for loop parity)
---

## Description
Work-loop control from the phone per `docs/design/ios-client.md` §6 phase 3 step 11 — blocked on 0179 (daemon-side loop; the exact RPC shape comes from that task).

## Description
- Start an unattended backlog drain from the app (per project), observe loop state (current session, tasks drained/blocked so far), and gracefully stop it (current task finishes; next not picked).
- Surface the loop's end-of-batch digest in the app (and via the existing ntfy digest notification).
- `⟳ loop` indicator on loop-owned sessions in the session list/view.

## Acceptance criteria
- A loop started from the app keeps running with the phone locked/suspended; reopening the app shows accurate loop state.
- Graceful stop behaves per the daemon-side loop semantics; digest visible after completion.
- View-model logic under `swift test`.

## Plan

Wire the phone to the daemon-side work loop (task 0179's RPCs — `StartWorkLoop` / `StopWorkLoop` / `GetWorkLoop`, already present in the committed generated Swift under `clients/ios/YccKit/Sources/YccProto`). No proto or Go changes: this is purely an iOS client task.

### 1. YccClient wrappers (`clients/ios/YccKit/Sources/YccKit/YccClient.swift`)
Add a `// MARK: - Work loop (task 0190)` section mirroring the existing wrapper style:
- `func startWorkLoop(project: String) async throws -> Ycc_V1_WorkLoopInfo`
- `func stopWorkLoop(project: String) async throws -> Ycc_V1_WorkLoopInfo?`
- `func getWorkLoop(project: String) async throws -> Ycc_V1_WorkLoopInfo?`
Stop/Get return `nil` when the response has no loop set (`message.hasLoop == false` — daemon returns an unset `loop` when no loop has run). Error mapping is the existing `Self.map`: an already-running loop arrives as `failedPrecondition`.

### 2. `WorkLoopModel` (new `clients/ios/YccKit/Sources/YccKit/WorkLoopModel.swift`)
Follow the `WorkstreamsModel` pattern exactly (injected source protocol so logic is testable headlessly, `@MainActor @Observable`):
- `public protocol WorkLoopSource: Sendable` with the three methods above; `extension YccClient: WorkLoopSource {}`.
- `public enum WorkLoopState: String { case running, stopping, finished, none, unknown }` parsed from `WorkLoopInfo.state` (`none` when there is no loop at all), with `title` labels and derived booleans `canStart` (none/finished), `canStop` (running), `isActive` (running/stopping), `shouldPoll` (== isActive).
- Model state: `project`, `loop: Ycc_V1_WorkLoopInfo?`, `state`, `isLoading`, `isBusy` (action in flight), `errorMessage`, `actionError`, `unauthorized`, and `hasDigest`.
- Actions: `refresh()`, `start()`, `stop()` — all guarding on `isBusy`, mapping `YccError.unauthorized` to the `unauthorized` flag and other errors to `actionError`/`errorMessage` exactly as `WorkstreamsModel.handle` does. `start()`/`stop()` apply the returned snapshot immediately (no extra round-trip) so the UI reflects the transition at once.
- Pure, unit-tested helpers (static where possible): state parsing; `currentSessionID` (empty between sessions); `digestSections(for:)` → ordered `[WorkLoopDigestSection]` (Completed / Blocked / In review / Created, each with title, systemImage, rows), skipping empty groups; `summaryLine(for:)` (e.g. "3 sessions · 2 completed, 1 blocked"); `totalsLine` / cost formatting that honours `costStatus` (`partial`/`unpriced` → mark the figure as approximate/unpriced rather than pretending it is exact); `startedAtDate` via the same ISO8601 parsing style used by `SessionListModel.parseTimestamp` (reuse it if it can be made accessible without widening API surface unnecessarily; otherwise a small local equivalent).

### 3. `⟳ loop` marker on loop-owned sessions
- Extend `SessionListSource` with `func workLoop(project: String) async throws -> Ycc_V1_WorkLoopInfo?` AND give it a default implementation in a `extension SessionListSource` returning `nil`, so existing test mocks keep compiling.
- `SessionListModel.refresh()`: in the same per-project fan-out that loads history, also fetch the project's loop snapshot; failures are ignored (never degrade the session list, no partial warning). Store the union of `loop.sessions.map(\.sessionID)` plus a non-empty `loop.currentSessionID` as `loopSessionIDs: Set<String>` (rebuilt per refresh), and expose `public func isLoopSession(_ session:) -> Bool` (plus `isLoopSession(sessionID:)`).
- `LandingView.SessionRow`: when the row is loop-owned, show a small `arrow.triangle.2.circlepath` badge labelled "loop" next to the status badge (accessibility label "work loop session"). Same badge in `SessionView`'s header if it is cheap to plumb; otherwise skip it there (list/row is the acceptance criterion's focus).

### 4. `WorkLoopView` (new `clients/ios/App/WorkLoopView.swift`) + navigation
- Add `case workLoop(project: String)` to `HomeDestination` (`clients/ios/App/WorkspaceDrawer.swift`) and route it in `LandingView.destination(_:)`; add a "Work loop" entry (`arrow.triangle.2.circlepath`) to the project-actions overflow menus in both `LandingView.projectDestinations` and the matching menu in `SessionView`.
- Screen content:
  - Header card: state pill (running/stopping/finished/not running), started-at relative time, sessions-run count, totals (tokens + priced cost with the price-status caveat), and the `outcome` line once finished.
  - "Current session" row when `currentSessionID` is non-empty → pushes a live `SessionView` for it (same `navigationDestination(item:)` pattern `WorkstreamsView` uses for `liveTarget`).
  - Sessions-run list (id prefix + focus task + tokens/cost), each tappable into its transcript.
  - Digest sections from `digestSections(for:)` — task id, title, short sha, verdict tally, tokens/cost; blocked rows show `reason`.
  - Actions: **Start loop** behind a confirmation dialog (unattended spend — spell out that it drains ready backlog tasks until none remain or a budget cap trips) and **Stop loop** (graceful; dialog explains the current session finishes and no next one is picked). Buttons disabled per `canStart`/`canStop`/`isBusy`.
  - Empty state (`ContentUnavailableView`) when no loop has ever run, with the Start action.
  - Poll while on screen: `.task` loop that refreshes, then sleeps ~5s and refreshes again while `model.shouldPoll` (cancellation-aware, and stop polling once finished); also refresh on `scenePhase == .active` so reopening the app after suspension shows accurate state (the acceptance criterion), and `.refreshable` pull-to-refresh.
  - `.onChange(of: model.unauthorized)` → `app.handleUnauthorized()`, and an alert for `actionError`, mirroring `WorkstreamsView`.

### 5. Tests (`clients/ios/YccKit/Tests/YccKitTests/WorkLoopModelTests.swift`, plus additions to `SessionListModelTests.swift`)
Mock `WorkLoopSource` (in-memory, scriptable errors):
- refresh with no loop → `state == .none`, `canStart`, `!shouldPoll`;
- refresh with a running loop → state/currentSessionID/summary correct, `shouldPoll`, `canStop`, `!canStart`;
- `start()` applies the returned snapshot; a second start failing `failedPrecondition` surfaces the daemon message in `actionError` and leaves the previous snapshot intact;
- `stop()` flips to stopping/finished from the returned snapshot;
- `unauthorized` mapping on both refresh and actions;
- digest helpers: section ordering, empty groups skipped, counts/summary line, blocked reason preserved, cost formatting for `priced` vs `partial`/`unpriced`;
- unknown `state` string → `.unknown` (no crash).
SessionListModel: a mock returning a loop snapshot marks its sessions (including `currentSessionID`) as loop-owned; a throwing `workLoop` leaves the session list intact with no partial warning; loop ids are scoped per refresh (a stopped loop's ids disappear).

### 6. Docs
Update `docs/design/ios-client.md` §6 phase 3 step 11 to describe what shipped (start/observe/stop + digest + `⟳ loop` marker, GetWorkLoop polling) instead of "Blocked on that daemon work". Leave §9's decision text as the historical record.

### Verification note
This workspace has no Swift toolchain (memory: Linux box, connect-swift is Apple-only), so `swift test` and `xcodebuild` cannot run here. Keep the code conservative and self-consistent (no new dependencies, Swift 5.9/iOS 17 features only, match existing file style), double-check generated-symbol names against `clients/ios/YccKit/Sources/YccProto/ycc/v1/ycc.pb.swift` (field names, `hasLoop`, `Int64`/`Double` types) rather than guessing, and report clearly that Swift verification must run on the user's Mac. `go build ./... && go test ./...` is untouched by this change but run `git status` to confirm no Go/proto files were modified.

## Work log
- 2026-08-06 plan: Wire the phone to the daemon-side work loop (task 0179's RPCs — `StartWorkLoop` / `StopWorkLoop` / `GetWorkLoop`, already present in the committed generated Swift under `clients/ios/YccKit/Sources/Y
…[truncated]
- 2026-08-06 implementer report: Implemented task 0190’s iOS work-loop control end to end.  Changes: - Added `YccClient` wrappers for `StartWorkLoop`, `StopWorkLoop`, and `GetWorkLoop`, including unset-loop handling via generated `
…[truncated]
- 2026-08-06 review tier: single-opus — reviewers: sol
- 2026-08-06 review (sol): accept — The change correctly wires the existing daemon work-loop RPCs into the iOS client, adds a testable observable model with lifecycle/digest/cost helpers, implements start/observe/graceful-stop UI with f
…[truncated]
- 2026-08-06 revision: Applied the two requested hardening touch-ups only:  - In `SessionListModel.refresh()`’s no-registered-project branch, bound `let source = source` before both `async let` operations so the concurren
…[truncated]
- 2026-08-06 decision: accept — commit: ios: work-loop control — start/observe/stop the daemon loop + digest (task 0190)  Wire the phone to the daemon-side work loop RPCs (task 0179): YccClient wrappers for StartWorkLoop/StopWorkLoop/GetW
…[truncated]
- 2026-08-06 usage: 3,801,844 tok (in 497,637, out 50,447, cache_r 5,146,303, cache_w 136,614) · cost n/a (unpriced)
  implementer: 3,083,250 tok (in 289,161, out 24,937, cache_r 2,769,152, cache_w 0) · cost n/a (unpriced)
  reviewer:sol: 704,576 tok (in 208,440, out 11,528, cache_r 484,608, cache_w 0) · cost n/a (unpriced)
  coordinator: 14,018 tok (in 36, out 13,982, cache_r 1,892,543, cache_w 136,614) · cost n/a (unpriced)
