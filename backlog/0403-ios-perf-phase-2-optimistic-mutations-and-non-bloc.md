---
id: "0403"
title: 'iOS perf phase 2: optimistic mutations and non-blocking menu actions'
status: in_review
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
- 2026-09-28: implemented A–H (uncommitted, on top of uncommitted 0402). New YccKit AppDataCache (per-connection stale-while-revalidate cache, generation-guarded, cleared on connect/profile switch/disconnect/unauthorized); optimistic answers (incl. text-sent-while-question-pending = answer, matching daemon SendInputMessage), interrupt/resume/stop/rollover pending state cleared by durable acks with 8s fallback, optimistic user bubbles (15s echo timeout, Retry/Edit on failure), resume-from-list navigates immediately with a single snapshot, backlog/workstreams/settings optimistic with rollback + background reloads, cached screens on re-entry, usage load supersession, redundant GetWorkLoop removed, fixed spurious SetThinking/SetRoleConfig on settings-sheet load. Linux harness (/tmp/ycc-0402-real): 466 tests, only the 2 known Linux-only failures. Logic review (claude) blocker + should-fixes addressed; two App compile-level manual reviews (opus) found no errors — App/ still never compiled. Known limits: failed batch answers lose typed text; a server switch via deep link keeps the old LandingView model (pre-existing) so other screens fetch projects themselves. Remaining: Xcode build + swift test on Mac and on-device checks (question sheet dismissal + forced failure, bubble/echo replacement incl. duplicate text and steers, pause/stop banners over a dropped stream, resume-then-back, profile switch, rapid backlog status changes).
