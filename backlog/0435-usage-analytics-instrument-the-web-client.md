---
id: "0435"
title: 'Usage analytics: instrument the web client'
status: todo
priority: 2
created: "2026-10-08"
updated: "2026-10-08"
depends_on:
    - "0434"
spec_refs:
    - docs/design/usage-analytics.md#Kinds
---

## Description
Instrument clients/web per docs/design/usage-analytics.md.

## Acceptance criteria
- `src/app/analytics.ts`: in-memory buffer, flush every ~30s / at 100 events / on `visibilitychange` hidden (fetch keepalive with bearer), silent on failure; random visit id per page load; `visit` event with layout/theme/input attrs.
- Views: route pattern → shared view name (no ids) with dwell + `from`; modal surfaces (palette, help, new session, quick capture, confirm dialogs) count as views.
- Actions: AppAction registry runs record `via` (shortcut/palette/click); catalog of registered actions (+ shortcut labels) and views sent once per visit; important non-registry controls tracked (transcript fold/expand, jump-to-latest, composer send/attach/steer, interrupt/stop/resume, question answers, backlog edits/promote, workstream integrate/discard, settings changes, file browser navigation).
- Flows: new session, quick capture, task edit record open/submit/cancel.
- Errors: user-visible toasts/inline errors record operation + Connect code.
- Unit tests for buffering/naming; `scripts/web-build.sh` and committed dist.

## Work log
