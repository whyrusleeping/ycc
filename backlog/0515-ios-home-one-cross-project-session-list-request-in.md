---
id: "0515"
title: 'iOS home: one cross-project session-list request instead of 2 RPCs per project (reliable reopen)'
status: in_review
priority: 2
created: "2026-11-09"
updated: "2026-11-10"
depends_on: []
spec_refs:
    - docs/design/ios-client.md
    - docs/remote-api.md
---

## Description
**Evidence (2026-11-09, iOS analytics since 10-14):** 70 of 1,082 iOS visits recorded RPC failures, almost always at the moment of foregrounding, in bursts of about 20 at the same instant: `rpc.ListSessionHistory` 209 (deadline_exceeded 118, unavailable 91) and `rpc.GetWorkLoop` 111. The home list fans out two unary calls per project (`SessionListModel.addFetches`: ListSessionHistory + GetWorkLoop), so 22 concurrent requests for 11 projects, each with the 20s unary deadline. On a cold or tunnelled mobile link they fail together, and the list stays stale or shows errors. The web client fans out the same way (`clients/web/src/api/queries.ts`).

**Change:**
- Add one daemon RPC (or a cross-project mode of ListSessionHistory) that returns the merged first page across all registered projects, with pinned/follow-up rows and per-session work-loop membership. One round trip replaces 2×N.
- iOS home uses it: show the cached list immediately on foreground, refresh with the single call, and back off quietly on failure instead of retrying every project. Per-project pagination keeps working for scoped views.
- Web may adopt it, but that isn't required for this task.

**Acceptance:**
- A home refresh issues one list request regardless of project count (test with a fake source).
- Ordering, pinned and unread rows, and loop badges match the current merged result (shared fixture).
- Foregrounding with the daemon unreachable shows the cached list with a quiet stale indicator and records at most a few errors per visit.
- Proto regenerated for Go/web/Swift and committed.
- Verified on the configured Mac simulator. Left in_review for on-device use.

## Acceptance criteria

## Work log

- 2026-11-10 — Implemented. New `ListHomeSessions` RPC (internal/server/server.go): ListProjects-equivalent `projects` plus one `ProjectSessionPage` per distinct workspace (first `ListSessionHistory` page, `pinned`, `next_cursor`, `loop_session_ids`, per-project `error`). Proto regenerated for Go, web (ycc_pb.ts; internal/web/dist rebuilt) and Swift. iOS `SessionListModel` refreshes with that single call (retried twice like other foreground calls), feeding the existing merge, so ordering/frontier/pinned/unread/loop badges and per-project load-more are unchanged; on `unimplemented` it falls back to the old fan-out for the model's lifetime. A failed refresh keeps loaded rows and sets `staleReason` (list shows "Couldn’t refresh · updated …"); passive refreshes back off 5 s doubling to 60 s; pull-to-refresh always runs. Also fixed a legacy-path bug: late work-loop badges were dropped by the next load-more re-merge.
- Evidence: `go test ./...` green (new internal/server/home_sessions_test.go checks pages match per-project ListSessionHistory/GetWorkLoop, cursor continuation, empty registry). YccKit `swift test` on the Mac: 583 tests, 0 failures; new tests cover one request for 12 projects, shared-fixture parity with the fan-out (unscoped, each scope, after load-more), stale note + 3 requests per failed foreground + backoff, per-project error, older-daemon fallback, unauthorized, empty registry. App built for the iPhone Air simulator and checked against the sidebar-v2 scratch daemon via /debug/latency: connect → home load is a single `ListHomeSessions` (loop badge, needs-answer bell, follow-up section shown); with the tunnel cut, backgrounding and foregrounding kept the rows with the quiet stale note; with it restored, the next foreground issued one `ListHomeSessions` and the note cleared. Screenshots in .ycc/tmp/w0515/.
- Left in_review for on-device use.
