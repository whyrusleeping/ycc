---
id: "0290"
title: 'iOS: render workstream ready / needs_attention statuses'
status: todo
priority: 3
created: "2026-08-07"
updated: "2026-08-07"
depends_on:
    - "0251"
spec_refs:
    - docs/design/workstream-integration.md#7. Surfaces
---

## Description
Task 0251 introduced two new non-terminal workstream registry statuses that flow through `WorkstreamInfo.status`: `ready` and `needs_attention` (the latter with a reason recorded in the `workstream_needs_attention` event and the registry's `status_reason`). The iOS `WorkstreamStatus` enum (clients/ios/YccKit/Sources/YccKit/WorkstreamsModel.swift) currently maps unrecognized statuses to `.unknown`, so these rows render indistinctly.

## Acceptance criteria

- [ ] `WorkstreamStatus` gains `ready` and `needsAttention` cases mapped from the proto status strings ("ready", "needs_attention").
- [ ] Workstream rows visually distinguish "still working" (active/session status), "ready", and a loud "needs attention" (mirroring the TUI's ⚠ treatment).
- [ ] Merge affordances that gate on `active` also allow `ready` and `needs_attention` (parity with the daemon's InFlight semantics).
- [ ] YccKit tests updated (`swift test` on the user's Mac; ad-hoc signing per project memory).

## Work log
