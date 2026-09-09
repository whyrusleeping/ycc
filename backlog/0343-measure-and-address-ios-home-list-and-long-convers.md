---
id: "0343"
title: Cache daemon session summaries to remove repeated history decoding
status: done
priority: 2
created: "2026-09-06"
updated: "2026-09-09"
depends_on: []
spec_refs: []
---

## Description

Measure and remove repeated whole-log decoding from daemon session-history requests while preserving tolerant reduction, usage, live overlays, and ordering.

This completes the daemon summary-cache slice of the original iOS latency task. Remaining accepted scope is split into:
- [0376](0376-correlate-daemon-and-ios-latency-diagnostics-and-u.md): correlated daemon/iOS timing, bounded diagnostics, streaming lifetime, stage spans/counts/signposts, end-to-end measurements, and progressive home-list display without supplemental/all-project blocking.
- [0377](0377-add-bounded-history-and-tail-first-transcript-pagi.md): bounded history and tail-first transcript paging, cursor/deduplication/subscription/replay semantics, paging benchmarks, and Go/Swift protobuf/client integration.
- [0378](0378-measure-and-reduce-long-conversation-ios-replay-an.md): measured replay/rendering improvements and windowing, preserving eager-layout rationale, scroll anchoring, streaming, questions, unread/routing and replay correctness; Swift coverage and Mac Instruments verification.

## Acceptance criteria

- Measure representative synthetic histories before/after caching without claiming end-to-end iOS attribution.
- Cache only summaries and file metadata, safely across concurrent callers; prevent caller aliasing.
- Invalidate changed, replaced, and removed logs; do not cache unstable or failed/partial reads. Restart reconstructs a cold cache from disk.
- Preserve tolerant reduction, ordering, live status/waiting, and model/token/context usage.

## Outcome

Manager-owned summary cache validates log identity, size, and mtime before reuse and around reads. Summary slices are cloned; live overlays run on every request. Focused regressions cover invalidation, replacement/deletion, cold restart, caller isolation, malformed reads, live overlays and concurrency. Independent review accepted.

Synthetic Linux benchmark (AMD EPYC 9684X; 100 logs / 100,000 events / ~17.6 MB; three one-iteration runs): uncached 709–751 ms, cold cache 726–752 ms, warm cache 3.67–3.71 ms (~190–200x faster). Allocations fall from ~141.5 MB / 2.60M to ~222 KB / 1,448. Independent task-only checkout measured 726 ms uncached, 726 ms cold, 3.75 ms warm. Prior real-log audit: ~313 logs / 153 MB, ~2.5 seconds standalone JSON decode; not an iOS latency measurement.

Verification on the isolated task-only git tree passed: full `go test ./internal/session -count=1`, full `go test -race ./internal/session -count=1`, focused history race tests, and `BenchmarkSessionHistorySummaryCache` with `-benchmem`. Broader dirty-tree runs encountered unrelated race/environment failures; the isolated candidate passed. Existing unrelated uncommitted work was preserved and excluded from the commit.

Commit subject: `Cache unchanged session-history summaries`
