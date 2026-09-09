---
id: "0377"
title: Add bounded history and tail-first transcript paging across daemon and clients
status: todo
priority: 2
created: "2026-09-09"
updated: "2026-09-09"
depends_on:
    - "0343"
spec_refs: []
---

## Description
Split from accepted task 0343; summary caching stays in 0343.

Acceptance criteria:
- Bounded/paginated session-history reads and tail-first transcript paging with explicit cursor/deduplication semantics compatible with live subscription and replay.
- Preserve correct summaries, live status, unread/routing behavior, transcript replay and question visibility. Tail paging must recover any state required for correct replay rather than silently dropping context.
- Coordinate protobuf changes with BOTH Go and Swift generated client sets; integrate iOS consumption.
- Benchmark representative large histories/logs and paging; focused regressions cover page boundaries and subscription/replay overlap. Record measured results; Mac required for iOS runtime verification.

## Acceptance criteria

## Work log
