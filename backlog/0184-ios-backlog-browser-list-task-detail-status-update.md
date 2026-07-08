---
id: "0184"
title: 'iOS: backlog browser — list, task detail, status updates, quick capture'
status: todo
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

## Work log
