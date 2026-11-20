---
id: "0563"
title: Let mutating subagents send cross-session messages
status: proposed
priority: 4
created: "2026-11-20"
updated: "2026-11-20"
depends_on:
    - "0443"
spec_refs:
    - spec.md#7.3
---

## Description
Current state: proposed; deferred from 0443 (experimental cross-session messages), which limits `message_session` to coordinators (user decision, 2026-11-20, s_18f059896f075e5a).

## Idea
Let mutating subagents (implementers and mutating generic agents) send `message_session` to other top-level sessions in the project. A mutating subagent is often the first to know that a peer should hold off on a file.
- Sent as the parent session: sender is the session id, the actor is the subagent. Messages count against the session's per-receiver rate limit and use the session's hop count.
- Send-only. Replies and other incoming messages go to the coordinator, which relays them with `send_to_agent` when relevant.
- Read-only subagents stay excluded. Subagent-to-subagent messaging stays out of scope, since the coordinator relays between its own agents.

## Open question
Whether the coordinator should see a copy of each message its subagent sends, so it stays accountable for the task's outward communication.

## Acceptance (draft)
- Mutating subagents can send when the setting is on; the receiver sees the parent session as the sender.
- The session-wide rate limit and hop limit hold across the coordinator and its subagents.

## Acceptance criteria

## Work log
