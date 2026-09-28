---
id: "0403"
title: 'iOS perf phase 2: optimistic mutations and non-blocking menu actions'
status: todo
priority: 2
created: "2026-09-28"
updated: "2026-09-28"
depends_on:
    - "0402"
spec_refs: []
---

## Description
From 2026-09-28 audit. Mutations wait on RPC (often + full refetch) before the UI changes:
- Answer question sheets dismiss only after RPC; no in-flight guard (double tap → failed_precondition). Dismiss immediately, resolve gate optimistically, roll back + alert on error.
- Interrupt/Resume/Stop/Rollover: set local pending state immediately, disable buttons in flight.
- Send on persisted session: navigate/show optimistic user bubble; don't serialize ResumeSession → SendInput visibly.
- Resume from home swipe/menu and Start work: navigate immediately into a starting/resuming SessionView, run Resume in parallel with GetSessionView.
- Backlog setStatus: optimistic patch, clear busy after UpdateTask, background backlog-only refresh (no ListProjects). Quick capture: insert returned task and dismiss.
- Workstreams merge/discard/retry: use returned WorkstreamInfo instead of discard+refetch.
- Global settings SetThinking picker optimistic; model save doesn't block dismiss on ListModels. Check suspected spurious SetThinking on SessionSettings load.
- Screens rebuilt by HomeRouter (Backlog, Workstreams, WorkLoop, Usage, NewSession) render per-project cached data immediately and revalidate; take projects from SessionListModel instead of refetching ListProjects; cache modes/models app-wide.

## Acceptance criteria

## Work log
