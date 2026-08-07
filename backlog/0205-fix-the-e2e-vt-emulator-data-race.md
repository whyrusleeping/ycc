---
id: "0205"
title: Fix the E2E VT emulator data race
status: done
priority: 2
created: "2026-07-15"
updated: "2026-08-07"
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

## Plan

Problem: in internal/e2e/harness_test.go the PTY-reader goroutine mutates the VT emulator via `h.emu.Write` while the test goroutine calls `h.emu.CellAt(x,y)` (screenText) and then dereferences the returned `*uv.Cell` AFTER SafeEmulator's internal lock is released. Screenshots (snapshot.WriteScreenPNG via the Grid interface) and `h.emu.Resize` have the same exposure. `go test -race ./internal/e2e` flags it.

Fix: add a harness-level mutex that serializes ALL mutation of the emulator with all complete reads (lock held across the CellAt call AND the subsequent field reads/copies):

1. Introduce a small wrapper (e.g. `lockedEmulator` struct { mu sync.Mutex; emu *vt.SafeEmulator }) or an explicit `emuMu sync.Mutex` field on the harness — either is fine, but the invariant must be enforced at every access site:
   - PTY-reader goroutine: hold the lock around `emu.Write(buf[:n])`.
   - `screenText`: hold the lock for the whole grid walk, copying `Content`/`Width` while locked.
   - `resize`: hold the lock around `emu.Resize`.
   - `screenshot`: hold the lock for the duration of `snapshot.WriteScreenPNG(...)` (it calls CellAt repeatedly and reads cell fields), passing the underlying emulator as the Grid.
2. CRITICAL — do not reintroduce the terminal-query deadlock: the reply-drain goroutine (`h.emu.Read` → ptmx.Write) must NOT take the harness lock. `SafeEmulator.Write` can block writing query replies to its internal pipe while holding its own lock; the drain goroutine must keep draining independently. Verify `vt.SafeEmulator.Read` does not need external synchronization w.r.t. Write (check the vendored source in the module cache); document why Read stays outside the lock.
3. Document the locking invariant in a comment beside the harness emulator field: "every emulator mutation (Write/Resize) and every complete read (CellAt + field access, WriteScreenPNG) must hold emuMu; emu.Read (query-reply drain) intentionally does not."
4. Keep the existing four E2E scenarios untouched; no new sleeps.

Verification:
- `go test -race -count=10 ./internal/e2e` passes (PTY-capable env — confirmed by baseline run).
- `go test ./internal/e2e` (non-race) still passes.
- Full `go test -race ./...` in the background; note known-flaky packages (internal/session, internal/setup, internal/tools) — verify any failure against HEAD before blaming this change.

### Starting points
- internal/e2e/harness_test.go — all access sites: PTY reader goroutine (~line 116), reply drain (~135, must stay lock-free), screenText (~233), resize (~294), screenshot (~317)
- internal/tui/snapshot/snapshot.go:112 Grid interface {CellAt(x,y) *uv.Cell}; RenderScreen reads cell fields after CellAt returns
- vt.SafeEmulator source: github.com/charmbracelet/x/vt in the Go module cache — check whether Read touches the same mutex as Write

## Work log
- 2026-08-07 plan: Problem: in internal/e2e/harness_test.go the PTY-reader goroutine mutates the VT emulator via `h.emu.Write` while the test goroutine calls `h.emu.CellAt(x,y)` (screenText) and then dereferences the re
…[truncated]
- 2026-08-07 context hints: 3 recorded with plan
- 2026-08-07 context hints: internal/e2e/harness_test.go — the only file that should need changes; internal/tui/snapshot/snapshot.go:107-160 — Grid interface + RenderScreen's CellAt usage; vt.SafeEmulator: find source via `g
…[truncated]
- 2026-08-07 preload: 1 file(s), ~13 KiB seeded into implementer context
- 2026-08-07 implementer report: Implemented task 0205 in `internal/e2e/harness_test.go`.  Changes: - Added a harness-level `emuMu` mutex and documented its invariant beside the emulator field. - Serialized PTY-driven emulator writes
…[truncated]
- 2026-08-07 review tier: single-opus — reviewers: sol
- 2026-08-07 review (sol): accept — The harness-level mutex correctly serializes PTY-driven emulator writes, resize mutations, complete screenText cell reads, and screenshot rendering. The terminal-query reply drain intentionally remain
…[truncated]
- 2026-08-07 decision: accept — commit: Fix E2E VT emulator data race with harness-level emulator mutex (task 0205)
