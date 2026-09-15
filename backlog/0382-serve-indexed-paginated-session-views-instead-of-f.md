---
id: "0382"
title: Serve indexed, paginated session views instead of full-log client replay
status: blocked
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

## Plan

Implement additive indexed presentation API and switch iOS, as explicitly approved. 1) Inspect durable append/subscription boundaries and existing iOS projection to define stable row/state/detail and update contracts; keep JSONL authoritative and existing APIs intact. 2) Add a versioned per-workspace SQLite projection with incremental durable catch-up, transactional watermark/state/row changes, crash/schema rebuild, bounded pages/details and sequence-safe update cursors (including old-row edits). No request-time full-history refold after backfill. 3) Add snapshot/page/detail/update RPCs, regenerate Go and Swift, and wire lifetime/confinement/cancellation. 4) Switch iOS initial load, earlier paging, expansion details and reconnect to presentation rows/state; preserve pending questions, controls, actor identity/live tails. 5) Verify concrete persistence/pagination/handoff regression risks, measure cold/warm opening and payload on real vals log, update durable API docs, review architectural changes comprehensively. Do not claim device timing or Swift build if tooling unavailable.

### Starting points
- internal/server/server.go and internal/session: transcript/subscription and durable emitter boundary
- clients/ios/YccKit/Sources/YccKit/{SessionProjection,SessionViewModel,SessionTranscriptSource,YccClient}.swift
- proto/ycc/v1/ycc.proto; regenerate both Go and Swift bindings
- Workspace initially clean except the new task 0382 backlog file. CONTRIBUTING.md requires proportional changes.

## Work log
- User explicitly approved including both daemon/API and the iOS switch in this task (2026-09-15).
- Implementation has not started: the delegation tool refused because this task file was untracked at the session's immutable baseline and task updates triggered its dirty-path ownership guard. User approved committing only this definition/plan. Unblock in a new session with a clean baseline (not a resume of the old session), set in_progress, and implement the saved plan.
