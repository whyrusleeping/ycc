---
id: "0436"
title: 'Usage analytics: instrument the iOS app'
status: in_review
priority: 2
created: "2026-10-08"
updated: "2026-10-08"
depends_on:
    - "0434"
spec_refs:
    - docs/design/usage-analytics.md#Kinds
---

## Description
Instrument clients/ios per docs/design/usage-analytics.md.

## Acceptance criteria
- YccKit `UsageAnalytics` recorder (pure, testable in the Docker swift setup): buffer, flush on ~30s timer / 100 events / scenePhase background, silent failure; visit id per foreground.
- Views: HomeRouter.open and NavigationLink drill-ins, sheets (new session, quick capture, session settings/usage, drawer, connect) with dwell + `from`, shared view names.
- Actions with `via` (click/swipe/context_menu/gesture/keyboard): composer send/attach/steer, interrupt/stop/resume, question answers, transcript expand/jump-to-latest, backlog promote/edit, drawer navigation, workstream actions, pull-to-refresh.
- Flows open/submit/cancel for new session, quick capture, task edit; user-visible errors with code.
- Status in_review until used on device.

## Work log
