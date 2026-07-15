---
id: "0205"
title: Fix the E2E VT emulator data race
status: todo
priority: 2
created: "2026-07-15"
updated: "2026-07-15"
depends_on: []
spec_refs:
    - Client UI (TUI)#Snapshot rendering for debugging (dev/test aid)
---

## Description
`go test -race ./...` reports a race in `internal/e2e`: the PTY reader mutates the VT emulator while `screenText` calls `CellAt` and then reads fields from the returned pointer after `SafeEmulator` has released its lock. The ordinary suite passes, but the repository cannot use its complete race suite as a reliable gate.

Take atomic whole-screen snapshots or add harness-level synchronization around emulator mutation and complete reads without reintroducing the known terminal-query deadlock.

## Acceptance criteria
- [ ] `screenText`, screenshots, resize handling, and emulator writes cannot concurrently access mutable VT cells.
- [ ] The reply-drain goroutine continues to drain emulator terminal-query responses; the harness does not deadlock.
- [ ] Existing four E2E scenarios continue to pass without bare synchronization sleeps beyond the current predicate loop.
- [ ] A focused repeated race run such as `go test -race -count=10 ./internal/e2e` passes.
- [ ] Full `go test -race ./...` passes in a PTY-capable environment.
- [ ] Any required wrapper/locking invariant is documented beside the harness fields.

## Work log
