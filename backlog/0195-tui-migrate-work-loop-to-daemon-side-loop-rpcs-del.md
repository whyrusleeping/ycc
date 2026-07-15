---
id: "0195"
title: 'TUI: migrate work (loop) to daemon-side loop RPCs; delete client driver'
status: todo
priority: 2
created: "2026-07-15"
updated: "2026-07-15"
depends_on:
    - "0179"
spec_refs:
    - 9. Modes (the home menu)
    - "20.6"
    - docs/design/ios-client.md#9. Daemon-side work loop (decision, prerequisite for loop parity)
---

## Description
Migrate the TUI's `work (loop)` from its client-side driver to the daemon-side loop RPCs added in 0179 (`StartWorkLoop`/`StopWorkLoop`/`GetWorkLoop`), deleting the client loop machinery while preserving the UX.

Context: 0179 moves the loop driver into the daemon (engine + RPCs + no-progress guard + budget caps + digest). This task retires the now-redundant TUI driver and points the UI at the daemon.

## Scope
- Delete the client-side loop driver in `internal/tui/tui.go`: `loopNext`, `applyLoopDecision`, `loopDecisionMsg`, the `loopStopping`/`loopStarted`/`loopPrevFP`/`loopRun` bookkeeping, `snapshotLoopSession`, `buildLoopDigest`/`applyUsage`/`fetchLoopUsage`/`notifyLoopDigest` and the client-side no-progress/budget-cap enforcement — replaced by daemon RPC calls.
- Wire the home-menu **tab** toggle to `StartWorkLoop` and in-session **shift+tab** toggle to `StartWorkLoop`/`StopWorkLoop` (graceful). Observe loop state by polling `GetWorkLoop` (and Subscribe to the current session id it reports) so the `⟳ loop` indicator and advancing-through-sessions UX are preserved.
- Render the end-of-batch **digest** from the daemon-provided `WorkLoopInfo` (completed/blocked/in_review/created tasks, per-session tokens/cost) instead of building it client-side; keep the re-openable digest view.
- Reconnect behaviour: on entering a project with a running loop, GetWorkLoop lets the TUI re-attach and gracefully stop it.
- Keep `GetBudget` usage only where still needed for display; loop-cap enforcement is now daemon-side.

## Acceptance criteria
- TUI `work (loop)` UX preserved (tab/shift+tab toggles, `⟳ loop` indicator, end-of-batch digest view) driven entirely by the new RPCs.
- The client-side loop driver code is removed; no client-side no-progress/budget-cap/digest logic remains.
- A loop started from the TUI keeps running across a TUI restart; reconnecting re-attaches and can gracefully stop it.
- TUI tests updated/added for the RPC-driven flow; `go build ./...` and `go test ./...` green.

## Work log
