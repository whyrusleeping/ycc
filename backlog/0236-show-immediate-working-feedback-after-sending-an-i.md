---
id: "0236"
title: Show immediate working feedback after sending an iOS message
status: done
priority: 2
created: "2026-08-05"
updated: "2026-08-06"
depends_on: []
spec_refs:
    - clients/ios
---

## Description
Improve the iOS session UI so that immediately after the user submits a message, it visibly indicates that the agent is working instead of remaining visually idle until the first agent event arrives.

## Acceptance criteria
- The message submission path immediately enters a visible in-progress state.
- The state transitions cleanly when streamed/persisted agent output, idle, a question, or an error arrives.
- Relevant YccKit/UI tests cover the behavior.

## Work log
- 2026-08-07: Closed done in a backlog audit — implemented and committed; on-device use is the verification the Linux box could not provide.
