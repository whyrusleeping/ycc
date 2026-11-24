---
id: "0606"
title: 'ListHomeSessions: loop_session_ids omits the other slots'' sessions, so iOS "via loop" shows on one live session'
status: todo
priority: 2
created: "2026-11-24"
updated: "2026-11-24"
depends_on: []
spec_refs:
    - spec.md#9.1
---

## Description
Current state: todo. Accepted by the user 2026-11-24 in session s_5a6bb9c25a12632a (UX review of the live wheeler work loop).

`homePage` in `internal/server/server.go` fills `ProjectSessionPage.loop_session_ids` from the loop's recorded `Sessions` plus `CurrentSessionID`. With parallel slots (0598, N parallel loop sessions in the shared tree) the other slots' live sessions, working or waiting, are not added.

Observed 2026-11-24 with the HEAD iOS app against wheeler loop `loop_d074bc58` (5 live loop sessions): the home list showed "work · via loop" on finished loop sessions and on the 0272 session (the loop's current one), and plain "work" on the 0348 and 0449 sessions.

## Acceptance criteria
- `loop_session_ids` includes the session of every slot in the loop snapshot, working or waiting, as well as recorded and current sessions, without duplicates.
- A server test covers a loop with two or more slots.
- The proto comment on `loop_session_ids` ("its recorded sessions plus the current one") matches the behaviour.

## Work log
