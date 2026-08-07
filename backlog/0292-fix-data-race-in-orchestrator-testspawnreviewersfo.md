---
id: "0292"
title: Fix data race in orchestrator TestSpawnReviewersFocusedTier (captureRec.Record)
status: todo
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

## Work log
