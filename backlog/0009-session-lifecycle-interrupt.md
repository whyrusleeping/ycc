---
id: "0009"
title: Session lifecycle — Interrupt RPC and stop/GC
status: done
priority: 3
created: "2026-06-26"
updated: "2026-06-28"
depends_on:
    - "0003"
spec_refs:
    - RPC protocol
    - Session & event log
---

## Description
`Session.Stop()` exists but is never called, and there is no Interrupt RPC (spec §12
lists one). Every started session's goroutine + agent loop lives for the daemon's whole
lifetime; an interactive session blocked on ask_user with no client to answer blocks
forever; a runaway autonomous session can't be halted. (Found in the 2026-06-26 review,
MAJOR #2.)

## Acceptance criteria
- [x] `StopSession(session_id)` RPC that calls `Session.Stop()` (cancels ctx, closes log) — distinct from the existing graceful `Interrupt` (spec §18.7)
- [x] manager removes stopped sessions from its map (no leak)
- [x] a session blocked in ask_user unblocks cleanly on stop (ctx cancel path)
- [~] consider GC/retention of idle sessions and on-disk logs — deferred to a follow-on backlog task (automatic idle GC + log retention)
- [x] TUI/CLI affordance to stop a session (TUI ctrl+x; `ycc stop <id>`)

## Outcome

Added the distinct hard-terminate `StopSession` RPC while preserving graceful Interrupt behavior. Session stopping became idempotent and records terminal state, cancels blocked work, and closes the log; manager/server removal and errors, CLI `ycc stop`, TUI ctrl+x, projection, cancellation, and lifecycle behavior were covered. Automatic idle GC and on-disk retention remained deferred.

Commit: c043d0c — Add StopSession RPC to hard-terminate sessions
