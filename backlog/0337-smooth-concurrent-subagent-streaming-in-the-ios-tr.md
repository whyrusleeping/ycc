---
id: "0337"
title: Smooth concurrent subagent streaming in the iOS transcript
status: in_review
priority: 2
created: "2026-08-16"
updated: "2026-08-16"
depends_on: []
spec_refs:
    - Event log
    - docs/design/ios-client.md#Navigation and interaction
---

## Description
Improve the native iOS session transcript when several subagents stream concurrently. The projection must retain an independent transient tail per actor instead of replacing one global row, and the SwiftUI transcript should render/update those tails without causing competing rows to jump or disappear.

## Acceptance criteria

- Concurrent `turn_delta` events from distinct actors are visible as stable, independently updating live rows.
- A terminal delta or durable model turn clears only the matching actor's live tail.
- Reconnect/reset clears all transient tails without advancing the durable cursor.
- Headless Swift tests cover interleaved actor deltas and actor-scoped completion.
- The iOS transcript remains compatible with the existing single-stream behavior.

## Work log
