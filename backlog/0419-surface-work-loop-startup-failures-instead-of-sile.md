---
id: "0419"
title: Surface work-loop startup failures instead of silently finishing
status: in_review
priority: 2
created: "2026-10-03"
updated: "2026-10-03"
depends_on: []
spec_refs: []
---

## Description
Lamia's loop stopped before its first session because the Anthropic refresh token expired. The outcome was persisted, but zero-session failures sent no notification and the iOS backlog banner only showed Finished / zero sessions.

## Acceptance criteria
- A loop startup failure sends an error notification even when no session was created.
- iOS surfaces the terminal outcome after starting/polling a loop, and the backlog banner retains the stop reason.
- Normal empty-backlog completion is not reported as an error.

## Work log
- Lamia's persisted loop_f738a613 outcome reports an expired Anthropic refresh token while building the opus coordinator, before any session ran.
- Added daemon error notification for session startup failure; iOS completion message handles immediate terminal Start responses and active-to-finished polling, while the banner retains the outcome.
- Go work-loop/real-runner tests and race-enabled startup/empty-backlog tests pass; focused review found no confirmed blocker. Swift model regression tests added but not run here.
- Awaiting Xcode/on-device verification, including immediate startup failure after confirmation dismissal and interaction with an already-present action-error alert. Not deployed or committed.
