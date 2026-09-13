---
id: "0372"
title: Reconnect TUI subscriptions from the last durable event sequence
status: blocked
priority: 2
created: "2026-09-08"
updated: "2026-09-10"
depends_on: []
spec_refs:
    - §12 RPC protocol
    - §18.4 Reasoning and streaming
    - §18.6 Session history and reopen
---

## Description
The TUI subscribes without FromSeq and does not automatically resubscribe after a dropped stream. A remote network flap leaves a frozen transcript while the daemon continues work. Evidence: internal/tui/session.go:46–65; internal/tui/tui.go subscription/error handling; reconnect contract in docs/remote-api.md.

## Acceptance criteria
- Track the last applied durable seq and reconnect transient subscription failures with bounded cancellable backoff and FromSeq.
- Deduplicate replayed durable events; clear stale transient actor tails without advancing the durable cursor.
- Distinguish auth/not-found/terminal session errors from retryable network failures and expose appropriate recovery rather than endless retry.
- Surface reconnect state without losing input drafts, scroll position, questions, or user control.
- Cancel retry/subscription goroutines when switching sessions or exiting.
- Tests cover disconnect/replay/live continuation, repeated flaps, auth failure, daemon restart, cancellation, and no duplicate transcript rows.

## Work log

- Preflight: blocked in this workspace by pre-existing changes in `internal/tui/session.go`, `internal/tui/tui.go`, and related transcript/tests. The verified changeset ownership guard refuses editing baseline-dirty paths. No implementation attempted; start in an isolated clean task session or resolve the existing owners' changes before capturing a new baseline.
