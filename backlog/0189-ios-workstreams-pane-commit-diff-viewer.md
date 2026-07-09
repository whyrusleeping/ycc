---
id: "0189"
title: 'iOS: workstreams pane + commit diff viewer'
status: done
priority: 4
created: "2026-07-08"
updated: "2026-07-08"
depends_on:
    - "0182"
spec_refs:
    - "14.1"
    - docs/design/ios-client.md#6. Screens & feature phases
---

## Description
Workstreams and diff viewing per `docs/design/ios-client.md` §6 phase 3 step 10 (spec §14.1).

## Description
- Workstreams pane: `ListWorkstreams` with per-stream status; actions `PreviewMerge` (show summary/conflicts), `MergeWorkstream`, `DiscardWorkstream` (confirmation dialogs on destructive actions).
- Commit diff viewer: `GetCommitDiff` invoked from `commit_made` event rows in the session feed and from workstream detail; monospaced, syntax-tinted unified diff rendering, phone-scrollable.

## Acceptance criteria
- Listing, preview, merge, and discard round-trip against a daemon with live worktrees.
- Tapping a commit_made row shows its diff; large diffs scroll without hanging the UI.
- View-model logic under `swift test`.

## Plan

Workstreams pane + commit diff viewer for the iOS app (spec §14.1; design §6 phase 3 step 10).

1. YccKit:
   - YccClient wrappers: `listWorkstreams(project:)`, `previewMerge(workstreamId:)` → (clean, conflicts, diff), `mergeWorkstream(workstreamId:accept:)` → full response (merged/commit/needsAccept/diff/conflicts), `discardWorkstream(workstreamId:)`, `getCommitDiff(project:sha:)` → diff string.
   - `WorkstreamsModel` (@MainActor @Observable, source protocol): load/refresh list with per-stream status (status + session_status + commit_count), preview/merge/discard actions with in-flight state; merge flow honors the accept gate (clean+gated → expose diff + needsAccept so the UI shows the diff and a confirm; conflicts → expose conflicted paths); errors/unauthorized like the other models.
   - `DiffModel` or a pure `DiffFormatter`: parse a unified diff into colored line runs (header/hunk/add/del/context) for rendering; keep it pure and unit-tested. Handle large diffs by capping/or lazy rows (List of line rows is fine).
   - SessionProjection: expose the commit sha on commit_made rows (check what's there today; add a `commitSha` field to the row payload if missing) so the session view can open the diff viewer.
   - Tests: workstream status mapping, merge accept-gate state machine (needs_accept → accept), conflict surfacing, diff parsing (add/del/hunk/file headers), commit_made sha extraction.
2. App:
   - WorkstreamsView: List of workstreams (branch/task/status badges, commit count, session status), entry from LandingView toolbar; detail or swipe/context actions: Preview merge (sheet with summary → conflicts list or diff view), Merge (confirmation; if needsAccept show integrated diff with an Accept button that re-calls accept=true), Discard (destructive confirmation dialog). Also allow opening the workstream's session (session_id → SessionView live) — cheap and useful.
   - DiffView: monospaced, horizontally scrollable, color-tinted unified diff rendered as a LazyVStack/List of rows (phone-scrollable, no hangs on large diffs).
   - SessionView: tapping a commit_made row pushes DiffView via GetCommitDiff(project, sha).
   - Errors inline/alerts; unauthorized routes to connect.
3. Verify: swift test; xcodegen + xcodebuild simulator build; extend plans/ios-client-smoke.md with workstream + diff smoke steps.

### Starting points
- proto/ycc/v1/ycc.proto — WorkstreamInfo/ListWorkstreams/PreviewMerge/MergeWorkstream (accept gate: needs_accept + diff)/DiscardWorkstream/GetCommitDiff shapes
- clients/ios/YccKit/Sources/YccKit/SessionProjection.swift line ~492 — commit_made row handling (need sha exposed on the row for tap-through)
- clients/ios/App/SessionView.swift — transcript row rendering; hook commit_made tap → diff viewer
- clients/ios/YccKit/Sources/YccKit/UsageModel.swift + BacklogModel.swift — source-protocol/model/tests pattern
- clients/ios/App/LandingView.swift — toolbar entry-point pattern
- docs/remote-api.md — MergeWorkstream accept-gate semantics
- plans/ios-client-smoke.md — extend

## Work log
- 2026-07-08 plan: Workstreams pane + commit diff viewer for the iOS app (spec §14.1; design §6 phase 3 step 10).  1. YccKit:    - YccClient wrappers: `listWorkstreams(project:)`, `previewMerge(workstreamId:)` → (cl
…[truncated]
- 2026-07-08 context hints: 7 recorded with plan
- 2026-07-08 context hints: proto/ycc/v1/ycc.proto — WorkstreamInfo/ListWorkstreams/PreviewMerge/MergeWorkstream (accept gate: needs_accept + diff)/DiscardWorkstream/GetCommitDiff shapes; clients/ios/YccKit/Sources/YccKit/Sess
…[truncated]
- 2026-07-08 implementer report: Implemented the iOS workstreams pane + commit diff viewer (task 0189).  ## YccKit (headless, unit-tested) - **YccClient.swift**: added typed async wrappers `listWorkstreams(project:)`, `previewMerge(w
…[truncated]
- 2026-07-08 review tier: single-opus — reviewers: claude
- 2026-07-08 review (claude): accept — Task 0189 is implemented completely and correctly. YccKit gains typed wrappers for ListWorkstreams/PreviewMerge/MergeWorkstream/DiscardWorkstream/GetCommitDiff (field names verified against ycc.proto)
…[truncated]
- 2026-07-08 decision: accept — commit: iOS: workstreams pane (preview/merge accept-gate/discard) + commit diff viewer (task 0189)
- 2026-07-08 usage: 49,196 tok (in 176, out 49,020, cache_r 5,808,664, cache_w 278,530) · $7.1486
  implementer: 39,020 tok (in 108, out 38,912, cache_r 4,169,022, cache_w 115,924) · $3.7824
  reviewer:claude: 5,942 tok (in 48, out 5,894, cache_r 702,333, cache_w 50,138) · $0.8121
  coordinator: 4,234 tok (in 20, out 4,214, cache_r 937,309, cache_w 112,468) · $2.5541
