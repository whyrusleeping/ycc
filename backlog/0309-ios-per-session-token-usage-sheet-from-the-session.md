---
id: "0309"
title: 'iOS: per-session token usage sheet from the session screen'
status: in_review
priority: 3
created: "2026-08-09"
updated: "2026-08-09"
depends_on: []
spec_refs:
    - docs/design/ios-client.md#Usage
    - 20. Token usage & cost accounting
---

## Description
The session screen's overflow menu only links to the project-wide Usage screen; there is no way to see what the *current* session has spent (the TUI has the Σ status-bar readout, task 0062).

Add a "Session usage" item to the session action menu that presents a sheet showing the current session's token usage broken down by model, with cost, plus a total row — fed by `GetUsage(group_by: ["session","model"])` filtered client-side to the session id (the daemon has no session filter, but multi-dim grouping works).

Acceptance:
- YccKit `SessionUsageModel` (+ narrow `SessionUsageSource` protocol) with a pure, unit-tested breakdown/total helper (price-status merge: priced/unpriced/partial).
- `SessionUsageSheet` in the app reusing the existing `UsageRowView` rendering; loading / empty / error / unauthorized states; pull-to-refresh.
- Menu entry on live sessions; design doc note in docs/design/ios-client.md.
- Headless `swift test` passes on-device/Mac (no Swift toolchain here).

## Acceptance criteria

## Work log
