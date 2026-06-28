---
id: "0009"
title: Session lifecycle — Interrupt RPC and stop/GC
status: in_progress
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

## Work log
- 2026-06-28 plan: Note: the `Interrupt` RPC name is already taken by the graceful pause/steer feature (spec §18.7). So this task adds a *distinct hard terminate*: `StopSession`, which calls `Session.Stop()`, cancels t
…[truncated]
- 2026-06-28 done: Added a distinct `StopSession` RPC (proto + regenerated code) that hard-terminates a session. `Session.Stop()` is now idempotent (sync.Once), records a terminal `session_stopped` event (new event type → `StatusStopped`), cancels the ctx (unblocking any ask_user / checkpoint), and closes the log. New `Manager.Stop(id)` deletes the session from the map (no leak) and returns `ErrUnknownSession` for unknown/already-stopped ids. Server handler maps that to NotFound. CLI: `ycc stop <id>`. TUI: ctrl+x stops the session and returns to the menu (footer hint updated). Tests: reduce → StatusStopped; ask_user unblocks on Stop + idempotent session_stopped; Manager.Stop removes + ErrUnknownSession. Automatic idle GC + on-disk log retention deferred to a follow-on task. `go build ./...` and `go test ./...` pass.
- 2026-06-28 implementer report: Implemented task 0009 — a distinct hard "stop/terminate a session" capability (kept the existing `Interrupt` graceful pause/steer RPC untouched).  Changes: - **proto/ycc/v1/ycc.proto**: added `StopS
…[truncated]
- 2026-06-28 review tier: single-opus — reviewers: Claude
- 2026-06-28 review (Claude): accept — The change adds a distinct StopSession RPC (proto + regenerated connect/pb code) that hard-terminates a session, correctly kept separate from the existing graceful Interrupt. Session.Stop() is now ide
…[truncated]
