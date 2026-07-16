---
id: "0213"
title: 'iOS: allow continuing stale/reopened sessions'
status: done
priority: 2
created: "2026-07-15"
updated: "2026-07-15"
depends_on: []
spec_refs:
    - §4. Process & data-flow model
    - §5. Session & event log
---

## Description
Fix the iOS session detail flow so reopening a stale/idle session still exposes message composition and can resume the server-side session when the user sends another message.

Acceptance criteria:
- A reopened stale or idle session can accept and send a new user message from iOS.
- The client invokes the appropriate resume/input RPC semantics rather than treating historical terminal-ish state as permanently dead.
- Truly non-resumable states remain clearly represented.
- YccKit/iOS tests cover the stale-session continuation path.

## Acceptance criteria

## Work log
