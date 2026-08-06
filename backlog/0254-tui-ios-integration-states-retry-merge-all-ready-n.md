---
id: "0254"
title: 'TUI + iOS: integration states, retry, merge-all-ready, needs-attention notifications'
status: todo
priority: 3
created: "2026-08-06"
updated: "2026-08-06"
depends_on:
    - "0252"
spec_refs:
    - docs/design/workstream-integration.md#7. Surfaces
    - docs/design/ios-client.md#6. Screens & feature phases
---

## Description

Surface the extended lifecycle (task 0251/0252) in both clients so an auto-integrating
project is observable and its exceptions are one action away.

- **Row state** per workstream: working / ready / integrating / needs-attention / gated /
  merged, visually distinct (needs-attention loud).
- **Actions**: `RetryIntegration(workstream_id)` re-queues a `needs_attention` stream;
  "merge all ready" (one keystroke in the TUI, one button on iOS) for `mode = gate`; existing
  preview/merge/discard stay for `manual`.
- **Drill-in** reaches the `integrate` session's transcript, not just the work session.
- **Notifications**: ntfy on `needs_attention` (always) and merged (configurable), so an
  unattended run pings only when a human is actually needed.

Includes the `RetryIntegration` RPC + `WorkstreamInfo` integration-state fields, and the
YccKit-side model logic covered by headless tests (mirroring `WorkstreamsModelTests`).

## Acceptance criteria

- [ ] TUI Workstreams panel renders every new state and offers retry / merge-all-ready.
- [ ] iOS Workstreams pane does the same, with model logic unit-tested in YccKit.
- [ ] `RetryIntegration` re-queues and is idempotent for a stream already queued.
- [ ] A `needs_attention` transition emits a notification with a deep link to the workstream.


## Work log
