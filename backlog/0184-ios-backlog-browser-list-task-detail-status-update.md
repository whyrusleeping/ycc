---
id: "0184"
title: 'iOS: backlog browser — list, task detail, status updates, quick capture'
status: done
priority: 3
created: "2026-07-08"
updated: "2026-07-08"
depends_on:
    - "0180"
spec_refs:
    - "18.5"
    - docs/design/ios-client.md#6. Screens & feature phases
---

## Description
Backlog browser per `docs/design/ios-client.md` §6 phase 2 step 6 (TUI analog: spec §18.5).

## Description
- Backlog list from `ListBacklog` (optional project param): grouped/sectioned by status, ready/blocked annotations (ready flag, blockedBy ids), priority shown.
- Task detail from `GetTask`: frontmatter fields + markdown `body` rendered (native SwiftUI markdown is fine for the first cut).
- Status changes via `UpdateTask` (e.g. promote proposed → todo, mark blocked/todo) with a status picker.
- Quick capture: minimal `CreateTask` form (title + description) — phone-friendly idea capture.
- "Start work on this task" action → `StartSession` (mode work, task-focused prompt) once 0185 (start & resume sessions) lands; ship behind that dependency or as a follow-up wire-up if this merges first.

## Acceptance criteria
- List, detail, status update, and create all round-trip against a live daemon.
- Ready/blocked rendering matches ListBacklog semantics; empty backlog shows a sane empty state.
- View-model logic covered by `swift test`.

## Plan

Backlog browser for the iOS app (list, detail, status updates, quick capture, start-work action).

1. YccKit:
   - YccClient wrappers: `listBacklog(project:)` → [BacklogTaskSummary]; `getTask(project:id:)` → TaskDetail; `updateTaskStatus(project:id:status:)` → TaskDetail (UpdateTask with only `status` set); `createTask(project:title:body:)` → TaskDetail.
   - `BacklogModel` (@MainActor @Observable, source-protocol pattern): load/refresh, group tasks into status sections in a sensible order (in_progress/in_review, todo, blocked, proposed; done hidden or in a collapsed section per ListBacklog output — check whether daemon returns done tasks), expose ready/blockedBy annotations and priority; errorMessage/unauthorized handling like SessionListModel.
   - `TaskDetailModel`: load GetTask, expose frontmatter fields + markdown body; `setStatus(_:)` via UpdateTask (refreshes detail from response); status choices todo/in_progress/in_review/done/blocked/proposed per proto comment.
   - Quick-capture logic: validation (non-empty title) + `createTask` call (can live on BacklogModel).
   - Tests: grouping/sections ordering, ready/blocked annotation mapping, status update round-trip via stub, create validation + refresh, error surfacing.
2. App:
   - BacklogView: sectioned List (status sections; each row: id, title, priority badge, ready/blocked annotation like "[blocked by 0173]"), pull-to-refresh, empty state, toolbar "+" for quick capture sheet (title + description → CreateTask), navigation to TaskDetailView.
   - TaskDetailView: frontmatter (status pill, priority, deps, ready/blocked, dates) + markdown body rendered with native Text(.init())/AttributedString markdown per-block (keep it simple), status picker in toolbar/menu driving UpdateTask, and a "Start work on this task" button → StartSession (mode "work", task-focused prompt like "Work on task <id>: <title>", judgement level or last-used) then navigate into the live SessionView (reuse the 0185 pattern; may need the navigation target hoisted or a local navigationDestination).
   - Entry point: add a "Backlog" toolbar button or a top-level tab on LandingView — prefer a TabView (Sessions | Backlog) only if cheap; otherwise a toolbar list icon pushing BacklogView with project context (model.selectedProject).
   - Errors surfaced inline/alerts; unauthorized routes to connect.
3. Verify: swift test; xcodegen generate + xcodebuild simulator build; extend plans/ios-client-smoke.md with backlog smoke steps.

Check how ListBacklog treats done tasks (internal/daemon handler) before deciding section layout.

### Starting points
- proto/ycc/v1/ycc.proto lines 312-372 — ListBacklog/GetTask/UpdateTask/CreateTask shapes (UpdateTask uses optional fields; no-mutation request = refresh)
- clients/ios/YccKit/Sources/YccKit/YccClient.swift — wrapper + error-mapping style
- clients/ios/YccKit/Sources/YccKit/SessionListModel.swift — @Observable model + source-protocol test pattern
- clients/ios/App/LandingView.swift — NavigationStack home; LiveSessionTarget/navigationDestination pattern from 0185
- clients/ios/App/NewSessionView.swift — sheet + form style from 0185
- clients/ios/YccKit/Sources/YccKit/NewSessionModel.swift — startSession wrapper usage for the 'start work' action
- plans/ios-client-smoke.md — extend with backlog steps

## Work log
- 2026-07-08 plan: Backlog browser for the iOS app (list, detail, status updates, quick capture, start-work action).  1. YccKit:    - YccClient wrappers: `listBacklog(project:)` → [BacklogTaskSummary]; `getTask(projec
…[truncated]
- 2026-07-08 context hints: 7 recorded with plan
- 2026-07-08 context hints: proto/ycc/v1/ycc.proto lines 312-372 — ListBacklog/GetTask/UpdateTask/CreateTask shapes (UpdateTask uses optional fields; no-mutation = refresh); clients/ios/YccKit/Sources/YccKit/YccClient.swift �
…[truncated]
- 2026-07-08 implementer report: Implemented the iOS backlog browser (task 0184): list, task detail, status updates, quick capture, and start-work — all wired against the existing ListBacklog/GetTask/UpdateTask/CreateTask/StartSess
…[truncated]
- 2026-07-08 review tier: single-opus — reviewers: claude
- 2026-07-08 review (claude): accept — The iOS backlog browser (task 0184) is implemented completely and correctly. YccClient gains listBacklog/getTask/updateTaskStatus/createTask wrappers with consistent error mapping; BacklogModel and Ta
…[truncated]
- 2026-07-08 decision: accept — commit: iOS: backlog browser — sectioned list, task detail + markdown body, status updates, quick capture, start-work action (task 0184)
