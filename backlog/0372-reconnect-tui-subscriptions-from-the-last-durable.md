---
id: "0372"
title: Reconnect TUI subscriptions from the last durable event sequence
status: done
priority: 2
created: "2026-09-08"
updated: "2026-09-26"
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

## Outcome

The TUI tracks the last applied durable seq and resubscribes after transient stream drops with cancellable backoff (0.5s up to 30s, stopping after 10 failures). It resubscribes from lastSeq-1 so an idle stream proves it is live. Replayed events are deduplicated, and stale transient tails are cleared without moving the cursor. Auth, not-found and terminal errors stop retrying and offer ctrl+y recovery; not-found recovery reopens the session in place. Connection state appears in the status bar, and the draft, scroll position and pending question are kept. Subscriptions are cancelled on switch, q, Back Home, loop finish and quit. Real Connect-server tests cover these cases. go test -race ./internal/tui/... passes, and the standard review accepted it.

Commit: tui: reconnect dropped session subscriptions from the last durable seq with bounded backoff, dedupe replay, classified recovery (ctrl+y) (0372)
