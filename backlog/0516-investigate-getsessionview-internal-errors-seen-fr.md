---
id: "0516"
title: Investigate GetSessionView `internal` errors seen from iOS
status: todo
priority: 3
created: "2026-11-09"
updated: "2026-11-09"
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

## Acceptance criteria

## Work log
