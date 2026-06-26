---
id: "0009"
title: Session lifecycle — Interrupt RPC and stop/GC
status: todo
priority: 3
created: 2026-06-26
updated: 2026-06-26
depends_on: ["0003"]
spec_refs: ["RPC protocol", "Session & event log"]
---

## Description
`Session.Stop()` exists but is never called, and there is no Interrupt RPC (spec §12
lists one). Every started session's goroutine + agent loop lives for the daemon's whole
lifetime; an interactive session blocked on ask_user with no client to answer blocks
forever; a runaway autonomous session can't be halted. (Found in the 2026-06-26 review,
MAJOR #2.)

## Acceptance criteria
- [ ] `Interrupt(session_id)` RPC that calls `Session.Stop()` (cancels ctx, closes log)
- [ ] manager removes stopped sessions from its map (no leak)
- [ ] a session blocked in ask_user unblocks cleanly on interrupt (ctx cancel path)
- [ ] consider GC/retention of idle sessions and on-disk logs
- [ ] TUI/CLI affordance to stop a session

## Work log
