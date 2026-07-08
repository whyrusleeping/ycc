---
id: "0189"
title: 'iOS: workstreams pane + commit diff viewer'
status: todo
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

## Work log
