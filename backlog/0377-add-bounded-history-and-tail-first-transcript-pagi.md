---
id: "0377"
title: Add bounded history and tail-first transcript paging across daemon and clients
status: done
priority: 2
created: "2026-09-09"
updated: "2026-09-26"
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

## Outcome

ListSessionHistory gains additive limit/cursor/next_cursor/pinned (limit 0 = legacy unbounded; 500 cap; InvalidArgument on bad cursor; first bounded page pins all live sessions outside it). Order and keyset are defined over the wire's millisecond timestamps (last_activity desc, started_at desc, id asc). Go and Swift protos regenerated. iOS loads 50-row pages per project, merges pinned rows, hides rows below the aggregate frontier until paged, offers "Load older sessions", and discards older-page results invalidated by a refresh. TaskDetail uses a bounded page plus pinned; deep-link fallback stays unbounded. The existing tail-first transcript paging (c2f0f77) gained page-boundary and tail-page/subscription overlap regressions. Benchmarks on 1000 logs: 112,000 → 5,688 wire bytes, 26 → 23 ms. Review accepted after 3 rounds. Go build/vet/tests pass. Swift was only syntax-parsed on Linux; swift test and on-device checks need a Mac.

Commit: server+ios: bounded keyset ListSessionHistory pages (limit/cursor/next_cursor, pinned live rows), wire-precision ordering, iOS paged home with aggregate frontier and Load older; transcript paging overlap regressions and benchmarks (0377)
