---
id: "0190"
title: 'iOS: work-loop control — start/observe/stop the daemon-side loop, digest view'
status: todo
priority: 4
created: "2026-07-08"
updated: "2026-07-08"
depends_on:
    - "0179"
    - "0183"
spec_refs:
    - 9. Modes (the home menu)
    - docs/design/ios-client.md#9. Daemon-side work loop (decision, prerequisite for loop parity)
---

## Description
Work-loop control from the phone per `docs/design/ios-client.md` §6 phase 3 step 11 — blocked on 0179 (daemon-side loop; the exact RPC shape comes from that task).

## Description
- Start an unattended backlog drain from the app (per project), observe loop state (current session, tasks drained/blocked so far), and gracefully stop it (current task finishes; next not picked).
- Surface the loop's end-of-batch digest in the app (and via the existing ntfy digest notification).
- `⟳ loop` indicator on loop-owned sessions in the session list/view.

## Acceptance criteria
- A loop started from the app keeps running with the phone locked/suspended; reopening the app shows accurate loop state.
- Graceful stop behaves per the daemon-side loop semantics; digest visible after completion.
- View-model logic under `swift test`.

## Work log
