---
id: "0363"
title: Separate provider stream-stall detection from total turn timeouts
status: done
priority: 2
created: "2026-09-08"
updated: "2026-09-25"
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

## Outcome

Registry-built provider clients (gollama + Codex) use internal/llmhttp: no fixed total http timeout; SSE inactivity (default 5m, heartbeats count) → ErrStreamStalled; optional total backstop (default 1h) → ErrTotalTimeout; caller cancellation stays context.Canceled and isn't retried. Configurable via [transport]. Retries after partial generated output (text/thinking/tool args) capped at 2 total attempts by default ([retry] partial_max_attempts), flagged in retry events. Truncated streams are retryable errors, never committed success (Codex in-repo; gollama e19be63 pushed and pinned). Tests cover progress/heartbeats, stalls, cancellation, total deadline, partial exhaustion, truncation. Spec updated.

Commit: provider: separate SSE inactivity from total turn timeout, cap partial-output retries, reject truncated streams (0363, 0400)
