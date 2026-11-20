---
id: "0562"
title: Render cross-session messages in web, iOS and TUI clients
status: proposed
priority: 4
created: "2026-11-20"
updated: "2026-11-20"
depends_on:
    - "0443"
spec_refs:
    - spec.md#7.3
    - spec.md#18.5
---

## Description
Current state: proposed follow-up to 0443 (experimental `list_sessions` / `message_session` tools), which ships only generic client fallback rendering. Split out because it touches three clients and needs the iOS build/screenshot check on the Mac, separate from the daemon change.

## Idea
- Show `peer_message` / `peer_message_delivered` events in the receiver's transcript as a distinct "Message from s_… (task NNNN)" entry, queued vs delivered like steered user input, with a link that opens the sender session. Show the sender's `message_session` call as a compact "sent to s_…" row linking to the receiver.
- Make "who is on what" more visible: backlog browsers (web, iOS) badge a task held by a live session with that session and link to it.
- iOS Settings toggle for the experimental session-messages setting, using the RPC added in 0443 (the web toggle ships with 0443), with the same "enabling applies to new sessions" note.

## Acceptance (draft)
- Web, iOS and TUI render both event types distinctly and link sender and receiver sessions.
- iOS Settings shows and changes the session-messages setting.
- Backlog browsers show the holding live session on held tasks.
- iOS changes built and screenshotted on the configured Mac; web checked with typecheck and a Playwright screenshot.

## Acceptance criteria

## Work log
