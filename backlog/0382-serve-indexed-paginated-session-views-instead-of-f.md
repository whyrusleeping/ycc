---
id: "0382"
title: Serve indexed, paginated session views instead of full-log client replay
status: done
priority: 1
created: "2026-09-15"
updated: "2026-09-15"
depends_on: []
spec_refs: []
---

## Description

User reports large iOS sessions remain too slow after off-main replay, bounded initial rendering, protobuf transport, and provider-state omission. These reduce constants but still transfer/fold history proportional to total session size.

Approved direction: retain append-only JSONL as authoritative model replay/audit history; maintain a versioned, rebuildable daemon-side SQLite read index per workspace for session summaries, presentation rows, detail references, and indexed-through sequence. Initial session-view RPC returns current state plus a recent page bounded by both rows and bytes. Fetch earlier pages and full tool/message details on demand. Tail presentation updates from the exact snapshot sequence, including edits to older tool/question rows, without requiring clients to possess full historical reducer state.

Important invariants: index never outruns durable log; index can be rebuilt/backfilled after crashes or schema changes; stale indexes cannot masquerade as current snapshots; stable pagination and no replay/live gaps or missed updates across loaded/unloaded rows. Warm opening cost should scale with the requested page, not total log size. Bulk backfill must not silently recreate a full-log scan on every request.

Approved rollout: include both additive daemon APIs and the iOS consumer in this task; legacy transcript/export/model replay unchanged. Measure a real vals log for page bytes/time-to-first-page. Target hundreds of KB rather than multi-MB startup payload, with concrete latency budget established on user's network/device.

## Acceptance criteria

- Additive daemon session-view APIs serve current presentation state and recent/earlier rows bounded by row count and encoded bytes, with full message/tool details fetched on demand; legacy transcript/export/model replay stay unchanged.
- A versioned, rebuildable per-workspace SQLite index follows only durable JSONL events, catches up incrementally, and cannot return a stale index as a current snapshot. Warm reads do not scan/fold the full log.
- Stable cursors and sequence-safe presentation updates preserve edits to older tool/question rows, including rows not loaded when edited, without requiring full client reducer history.
- iOS opens via the indexed view, loads older pages and details on demand, and reconnects without replay/live gaps; existing session controls, pending questions, and concurrent live output remain functional.
- Measure cold indexing and warm first-page time/bytes against a real large vals log when available. Initial payload is bounded in hundreds of KB; device/network latency is reported separately rather than guessed.

## Outcome

Implemented versioned per-workspace SQLite read index, additive bounded snapshot/page/detail APIs, atomic sequence-safe live updates, durable post-fsync indexing, and iOS paginated/version-aware consumer with lazy details and pending controls. Legacy transcript/export/model replay unchanged. Both comprehensive reviewers accept. Full Go suite and focused race tests pass; Go/Swift protos regenerated. Real vals 48+ MB log: cold index 2.87s, warm page 3.63ms, 161 rows/377807 protobuf bytes. Swift build/device/network latency unverified: no Swift/Xcode/device available. Broad server race run exposed pre-existing SetProjects/poller race; focused indexed race tests pass.

Commit: Serve indexed paginated session views and switch iOS to bounded loading
