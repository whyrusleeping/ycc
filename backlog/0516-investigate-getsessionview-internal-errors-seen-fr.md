---
id: "0516"
title: Investigate GetSessionView `internal` errors seen from iOS
status: done
priority: 3
created: "2026-11-09"
updated: "2026-11-10"
depends_on: []
spec_refs: []
---

## Description

**Evidence (iOS analytics, 2026-10-14..11-09):** `rpc.GetSessionView` failed with code `internal` 20 times (out of 50 failures, the rest deadline/unavailable), all on the session screen. `internal` is a server-side failure, not a network one.

**Work:**
- Find the dates/sessions from `~/.local/state/ycc/analytics/events-*.jsonl` (kind=error, name=rpc.GetSessionView, attrs.code=internal).
- Correlate them with daemon logs and the session-view index (cf. 0510, stale rows when the server rebuilds an index).
- Reproduce and fix, or record why it was a transient state that is now handled.

**Acceptance:** the root cause is identified and fixed with a regression test, or documented as already fixed by a named commit. GetSessionView returns a specific Connect code instead of `internal` for expected states such as a rebuilding index or a missing session.

## Outcome

Root cause of iOS GetSessionView `internal` errors: requests in flight when iOS suspended the app failed after the response headers had arrived. connect-swift read the 200 with a missing gzip body as `internal` ("unexpected encoding"), and a missing or truncated plain body could pass as an empty success. RetryGuardHTTPClient now reports these as unavailable, deadline_exceeded or canceled. LatencyInterceptor now records the error's decoded code in usage analytics, so daemon errors no longer read as internal (400) or unimplemented (404). Daemon mapping was already specific: missing session not_found, unknown project invalid_argument; the index rebuild is synchronous. Regression tests in TransportDecodeTests; YccKit swift test (553) passes on the Mac.

Commit: iOS: report body-lost-after-headers as a transport failure, not internal; analytics records decoded daemon error codes (0516)
