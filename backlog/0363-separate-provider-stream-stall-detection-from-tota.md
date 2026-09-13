---
id: "0363"
title: Separate provider stream-stall detection from total turn timeouts
status: blocked
priority: 2
created: "2026-09-08"
updated: "2026-09-09"
depends_on: []
spec_refs:
    - §7.1 Provider boundary and history
    - §7.2 Loop, repair, and failure handling
    - §13 Models, credentials, and review tiers
---

## Description
The pinned gollama NewClient sets a fixed 300-second http.Client timeout, which covers streamed response bodies. A legitimate long high-effort turn may be cut off and repeatedly restarted. Codex has a different transport policy. Review confirmed the timeout path, not the frequency of long-turn failures in production.

## Acceptance criteria
- Make transport/turn timeout policy configurable through supported provider-client construction rather than an inaccessible hardcoded timeout.
- Distinguish user/session cancellation, total execution policy, and stream inactivity; actively progressing streams can exceed the former five-minute transport cap when policy permits.
- Stall detection works with provider heartbeat behavior and terminates genuinely dead streams without stranding goroutines.
- Retry policy accounts explicitly for failures after partial streaming and avoids unbounded repeated expensive attempts; do not discard provider state or silently claim success.
- Tests use controllable streams for prolonged progress, idle stalls, partial-output failure, cancellation, and retry exhaustion.
- Update the dependency and its pinned version if gollama changes are needed; record the chosen defaults and cross-provider differences.

## Plan

Inspect provider construction, streaming parsers, and engine retry behavior. Add a supported configurable HTTP client boundary to gollama if needed and pin a reproducible dependency revision; use a shared ycc transport policy for gollama and Codex that separates optional total-request limits from heartbeat-aware read inactivity and caller cancellation. Keep retry attempts bounded and explicitly handle partial-output failures without replaying side effects or claiming success. Cover prolonged progress, heartbeats, stalls, cancellation, partial failure, and exhaustion with controllable streams; document defaults and provider differences. Review lifecycle/concurrency behavior and test the isolated task-only tree before selectively committing, preserving all pre-existing workspace changes.

### Starting points
- internal/config/config.go: new client construction near 1751 and 1812
- internal/codex/codex.go: New uses 15-minute HTTP timeout
- internal/engine/loop.go: streamTurn/runTurn and internal/engine/retry.go
- Pinned gollama b9fcec4; clean sibling /home/why/code/gollama exists but external modifications/publishing need supported access.
- Pre-task worktree, index, HEAD and diff are backed up under /tmp/ycc-0363-baseline. Existing unrelated staged work must not be committed.

## Work log
- 2026-09-09 implementer report: BLOCKED — Task requires a supported gollama HTTP-client construction boundary: the pinned/upstream HEAD still hardcodes the private http.Client timeout, and ycc cannot override it without reflection/unsafe. The
…[truncated]
