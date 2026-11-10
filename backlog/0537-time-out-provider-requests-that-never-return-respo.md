---
id: "0537"
title: Time out provider requests that never return response headers
status: done
priority: 2
created: "2026-11-10"
updated: "2026-11-10"
depends_on: []
spec_refs:
    - §7.2 Loop, repair, and failure handling
---

## Description
**Observed 2026-11-10:** session s_48c1caa17e774808 was reopened at 09:37:14. Its history ended on the `finish` tool result, so `PendingResponse()` re-ran the model with about 233k tokens. The Anthropic request went out over one connection (about 736 KB sent, about 5 KB received, which is roughly a TLS handshake). The connection then went silent: no response headers, with TCP keepalives holding it open. The session showed "working" and logged no events. Both user messages sent during this time were recorded as `queued:true` corrections. They can only be delivered at the next checkpoint, which never comes.

**Cause:** in `internal/llmhttp/policy.go`, the 5-minute `StreamIdle` watchdog is armed only after `RoundTrip` returns, that is, once headers have arrived. Before headers, only the 1-hour `Total` deadline applies. A provider that accepts the request and never responds therefore blocks the turn for up to an hour with no retry and no visible error. 0363 (done) separated stall detection from the total budget but left this phase uncovered.

**Acceptance:**
- When no response headers arrive within the stream-idle budget after the request is written, the request fails with `ErrStreamStalled` (or a sibling error that is classified the same way). It is then retried by the normal retry policy, and the retry is visible as a transient Retry event.
- `transport.stream_idle_seconds = 0` still disables this, and caller cancellation still wins.
- An llmhttp unit test uses a server that accepts the request and never writes headers.

**Related, not in scope:** reopening a session that ended cleanly via `finish` re-runs a model turn just to answer the `finish` tool result, and user input queues behind it. Consider treating a recorded `session_idle` after the last tool result as a clean boundary.

## Acceptance criteria

## Work log
