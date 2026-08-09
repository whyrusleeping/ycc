---
id: "0303"
title: Work loop page should show wall clock time each session took
status: in_review
priority: 3
created: "2026-08-08"
updated: "2026-08-09"
depends_on: []
spec_refs: []
---

## Description

## Acceptance criteria

## Plan

Add per-session wall-clock duration to the daemon work loop and surface it on the iOS work-loop page (which is what "work loop page" refers to — session rows already exist only there; TUI digest shows only a count and stays unchanged).

1. Proto: add `int64 duration_secs = 6;` to `WorkLoopSession` in proto/ycc/v1/ycc.proto (with a short comment: wall-clock seconds the session took). Regenerate Go (`buf generate`, local plugins) and Swift (`buf generate --template buf.gen.swift.yaml`, remote BSR plugins — network; generated files live under clients/ios/YccKit/Sources/YccProto/).
2. Engine (internal/session/workloop.go):
   - `loopSessRec` gains `duration time.Duration`.
   - `realRunSession` records `start := time.Now()` at the top and sets `rec.duration = time.Since(start).Round(time.Second)` before returning (also in the error-return path? No — early error returns an empty rec that is never accumulated, so only the normal path needs it).
   - `WorkLoopSession` (snapshot struct) gains `DurationSecs int64`; `snapshotLocked` maps it (`int64(s.duration / time.Second)`).
3. Persistence (internal/session/workloop_persist.go): `persistedWorkLoopSession` gains `DurationSecs int64 \`json:"duration_secs,omitempty"\``; map in both persist (line ~100) and restore (line ~202) directions so restart restores durations.
4. RPC (internal/server/server.go, workLoopToProto around line 1116): map `DurationSecs`.
5. Go tests: extend the existing persist round-trip test (workloop_persist_test.go) and the RPC snapshot test (internal/server/workloop_rpc_test.go, Sessions fixture at line ~63) to cover the new field. If workloop_test.go's fake runSession makes it easy, assert duration flows through accumulate/snapshot.
6. iOS:
   - YccKit WorkLoopModel.swift: add a pure static helper, e.g. `static func durationText(secs: Int64) -> String?` returning nil for <=0 and compact "Xs" / "Xm Ys" / "Xh Ym" otherwise.
   - clients/ios/App/WorkLoopView.swift `workLoopSessionRow`: append the duration (when non-nil) to the session row's secondary line, e.g. joined with the totals line via " · ".
   - Add unit tests for the helper in clients/ios/YccKit/Tests/YccKitTests/WorkLoopModelTests.swift (0s, seconds-only, minutes, hours cases). Swift tests cannot run in this environment — they run on the user's Mac; just keep them consistent with existing test style.
7. Verify: `go build ./... && go test ./internal/session/ ./internal/server/ ./internal/tui/`; confirm `git diff` of regenerated protos only adds the new field.

Note: the tree holds other tasks' uncommitted iOS work in the same files — do not revert or reformat unrelated hunks. Task ends at in_review (on-device verification pending), uncommitted, per project convention.

### Starting points
- proto/ycc/v1/ycc.proto WorkLoopSession (~line 641)
- internal/session/workloop.go: loopSessRec, snapshotLocked (~184), realRunSession (~640)
- internal/session/workloop_persist.go: persistedWorkLoopSession (~38), persist ~100, restore ~202
- internal/server/server.go workLoopToProto ~1116
- clients/ios/App/WorkLoopView.swift workLoopSessionRow ~227
- clients/ios/YccKit/Sources/YccKit/WorkLoopModel.swift totalsLine helpers
- regen: buf generate; buf generate --template buf.gen.swift.yaml (buf is in ~/go/bin)

## Work log
- 2026-08-09 plan: Add per-session wall-clock duration to the daemon work loop and surface it on the iOS work-loop page (which is what "work loop page" refers to — session rows already exist only there; TUI digest sho
…[truncated]
- 2026-08-09 context hints: 7 recorded with plan
- 2026-08-09 context hints: proto/ycc/v1/ycc.proto WorkLoopSession message ~line 641 (fields 1-5 used); internal/session/workloop.go: loopSessRec ~93, snapshotLocked ~184, realRunSession ~640; internal/session/workloop_persist.g
…[truncated]
- 2026-08-09 preload: 2 file(s), ~15 KiB seeded into implementer context
- 2026-08-09 implementation: recorded, persisted, and exposed per-session duration through the RPC; added compact iOS duration formatting and session-row display. Go build and targeted tests pass; awaiting on-device iOS verification.
- 2026-08-09 implementer report: Implemented task 0303 and moved its backlog status to `in_review` pending on-device verification.  Changes: - Added `duration_secs` to `WorkLoopSession` and regenerated Go/Swift protobufs; generated d
…[truncated]
- 2026-08-09 review tier: simple (coordinator self-review)
- 2026-08-09 usage: 1,577,001 tok (in 459,159, out 14,482, cache_r 1,659,497, cache_w 20,944) · cost n/a (unpriced)
  implementer: 1,569,742 tok (in 459,137, out 7,245, cache_r 1,103,360, cache_w 0) · cost n/a (unpriced)
  coordinator: 7,259 tok (in 22, out 7,237, cache_r 556,137, cache_w 20,944) · cost n/a (unpriced)
