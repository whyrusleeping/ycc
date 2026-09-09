---
id: "0376"
title: Correlate daemon and iOS latency diagnostics and unblock partial home-list display
status: todo
priority: 2
created: "2026-09-09"
updated: "2026-09-09"
depends_on:
    - "0343"
spec_refs: []
---

## Description
Split from accepted task 0343. Complete the cross-stack latency instrumentation and home-list display portion; daemon summary caching is handled by 0343.

Acceptance criteria:
- Correlated daemon RPC duration/outcome and iOS round-trip timings without payloads or credentials, with bounded retention; streaming lifetime tracked separately from request latency.
- Home-list load-stage and transcript decode/replay/first-display spans, event/row counts, and Instruments signposts.
- Measure representative large histories/conversations and record before/after results; distinguish daemon timings from end-to-end iOS attribution.
- Apply useful session-history results without waiting for supplemental work-loop snapshots or all other project loads; preserve live status, unread, routing, and refresh correctness.
- Targeted Go/Swift coverage as appropriate. On-device Instruments verification requires the user's Mac (no Swift toolchain here).

## Acceptance criteria

## Work log
