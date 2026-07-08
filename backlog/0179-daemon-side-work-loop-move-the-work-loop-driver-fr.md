---
id: "0179"
title: 'Daemon-side work loop: move the work (loop) driver from clients into the daemon'
status: todo
priority: 2
created: "2026-07-08"
updated: "2026-07-08"
depends_on: []
spec_refs:
    - 9. Modes (the home menu)
    - "20.6"
    - docs/design/ios-client.md#9. Daemon-side work loop (decision, prerequisite for loop parity)
---

## Description
Move the `work (loop)` unattended backlog drain from the client (TUI driver) into the daemon, per `docs/design/ios-client.md` §9 (user-accepted decision). Today the loop is a client concern (spec §9): the TUI starts the next `work` session when one finishes, enforces per-loop budget caps (§20.6), applies the no-progress guard, and accumulates the end-of-batch digest. A phone client cannot host that driver (iOS suspends backgrounded apps), and a loop that dies with its client is fragile even for the TUI.

## Description
- Design the RPC surface (part of this task): e.g. `StartWorkLoop` / `StopWorkLoop` (graceful — current session finishes, next not picked) and loop status visibility (loop id/state in `ListSessions`/`ListSessionHistory` or a `GetWorkLoop` RPC + loop lifecycle events). Keep spec §14's "no separate facade" posture — plain Connect RPCs.
- Move loop mechanics daemon-side: next-ready-task selection via fresh `work` sessions, the no-progress guard (stop if a finished session left its expected task unchanged), per-loop budget caps (§20.6 — currently "client-driven"), and the completion digest (pushed via the existing notifier `digest` kind, no client `Notify` call needed).
- Migrate the TUI to the new RPCs, deleting its client driver (tui loop* state) while preserving UX: tab toggles `work (loop)` on the home menu, shift+tab toggles the loop in-session, `⟳ loop` indicator, digest view at loop end.
- Update spec §9 (loop is no longer "a client concern"), §20.6 (loop cap enforcement moves daemon-side), and docs/remote-api.md (new RPCs).

## Acceptance criteria
- A loop started via RPC continues across client disconnects; reconnecting clients can observe and gracefully stop it.
- TUI loop UX preserved on the new RPCs; the client-side driver is removed.
- No-progress guard, loop budget caps, and digest work daemon-side, with tests.
- Spec + remote-api docs updated.

## Work log
