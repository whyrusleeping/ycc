---
id: "0380"
title: Keep large iOS session loads responsive and bound initial transcript rendering
status: in_review
priority: 1
created: "2026-09-14"
updated: "2026-09-14"
depends_on: []
spec_refs: []
---

## Description
Latest vals session (15,072 events, 43 MB log) loads locally from daemon in ~0.4s but iOS struggles. Full replay currently runs on MainActor and eager VStack mounts all thousands of projected rows.

Acceptance: large sessions initially show a bounded recent-history window with earlier history accessible; replay reduction does not block MainActor and stale/cancelled loads cannot publish; preserve stable rows, full lifecycle/question/cursor state and existing bottom-follow behavior. Do not replace eager stack with LazyVStack blindly (prior blank-view regression). Scope iOS loading, no wire/API changes required.

## Acceptance criteria

## Work log
- Confirmed latest vals log is valid (15,072 events, ~43 MB), no session errors; local daemon GetSessionTranscript completed in ~0.4s.
- Moved all bulk replay entry points to cancellable detached reduction with generation/revision checks before atomic publication. Initial fetch failures retry with backoff instead of bypassing bulk replay via Subscribe(0).
- Initially mount 200 durable rows; earlier pages retain stable row IDs and full projection state. Paging restores the previous first-row anchor and gates stale automatic bottom-follow callbacks.
- Added large replay/paging, hidden-tool pairing/reconnect, and transient-fetch retry coverage. Static review complete; git diff --check passes. Swift unavailable here: tests, app build, and device load/scroll checks remain outstanding. No wire changes or daemon restart required; install rebuilt iOS app to validate.
