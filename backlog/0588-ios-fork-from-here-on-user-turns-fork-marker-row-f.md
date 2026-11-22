---
id: "0588"
title: 'iOS: "Fork from here" on user turns, fork marker row, fork badge'
status: todo
priority: 3
created: "2026-11-22"
updated: "2026-11-22"
depends_on:
    - "0471"
spec_refs:
    - Session history and reopen
---

## Description
Status: todo. Delivers the iOS client part of 0471 (session fork and rewind from a user turn). 0471 shipped the daemon `ForkSession` RPC and the web action; iOS needs its own build-and-simulator verification on the Mac, so it is split out. The user accepted fork and rewind on 2026-11-13 (0471's acceptance); this split is the 0471 session's decision, 2026-11-22.

Today iOS renders a forked session's `session_forked` marker as a generic system row, and the YccKit projection does not treat the marker as idle, so a fork cut during a run shows as running until it is reopened.

## Acceptance criteria
- [ ] A user turn's context menu offers "Fork from here". It calls `ForkSession` with the turn's `user_input` seq, opens the new session, and puts the returned text in its composer draft.
- [ ] The YccKit projection treats `session_forked` as idle and clears a pending question. The row reads "Forked from <session>" and opens the parent when tapped.
- [ ] Session list rows show a fork marker when `forked_from` is set.
- [ ] Built and tested on the configured Mac (`swift test`, `xcodebuild` build and test), and checked in a simulator against a scratch daemon in light and dark mode.

## Work log
