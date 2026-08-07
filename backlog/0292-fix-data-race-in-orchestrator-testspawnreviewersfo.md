---
id: "0292"
title: Fix data race in orchestrator TestSpawnReviewersFocusedTier (captureRec.Record)
status: done
priority: 2
created: "2026-08-07"
updated: "2026-08-07"
depends_on: []
spec_refs: []
---

## Description
`go test -race ./internal/orchestrator` fails with a data race in `TestSpawnReviewersFocusedTier` at `modes_test.go:377-378` — concurrent access via `captureRec.Record`. Reproduced from an exact `git archive HEAD` during task 0205, so it is pre-existing (likely introduced with the configurable review tiers work, task 0285). It is the last known blocker to a clean full `go test -race ./...`, which task 0209 (CI gates) needs.

## Acceptance criteria
- [ ] The test's recorder (or production code, if the race is real there) is properly synchronized.
- [ ] Determine whether the race is test-only or reflects a real concurrency bug in reviewer-tier fan-out; fix production code if so.
- [ ] `go test -race -count=10 ./internal/orchestrator` passes.
- [ ] Full `go test -race ./...` passes (modulo known flaky packages).

## Plan

Diagnosis: the race is TEST-ONLY. runReviewers (internal/orchestrator/orchestrator.go:940-956) intentionally fans out reviewer goroutines that each call d.Emitter.Emit → Recorder.Record concurrently. The event.Recorder interface (internal/event/event.go:186-188) documents that implementations must be safe for concurrent use, and the production recorder (*event.Log) is mutex-protected. The test helper captureRec (internal/orchestrator/modes_test.go:374-380) violates the contract: it appends to a plain slice with no lock. No production concurrency bug.

Fix: add a sync.Mutex to captureRec; guard Record (seq assignment + append) and focusTasks (slice iteration). Direct rec.events reads in tests occur only after the tool Call returns (all reviewer goroutines joined via wg.Wait inside spawnReviewers), so happens-before is already established for those; guarding Record is what removes the race.

Verify: go test -race -count=10 ./internal/orchestrator passes; then full go test -race ./... (modulo known flaky packages per memory).

### Starting points
- internal/orchestrator/modes_test.go:373-390 (captureRec)
- internal/orchestrator/orchestrator.go:940-956 (runReviewers fan-out)
- internal/orchestrator/background_test.go:21-30 (syncRec precedent)
- internal/event/event.go:186-191 (Recorder contract: must be concurrency-safe)

## Work log
- 2026-08-07 plan: Diagnosis: the race is TEST-ONLY. runReviewers (internal/orchestrator/orchestrator.go:940-956) intentionally fans out reviewer goroutines that each call d.Emitter.Emit → Recorder.Record concurrently
…[truncated]
- 2026-08-07 context hints: 4 recorded with plan
- 2026-08-07 review tier: simple (coordinator self-review)
- 2026-08-07 decision: accept — commit: Synchronize captureRec test recorder to fix reviewer fan-out data race (task 0292)
