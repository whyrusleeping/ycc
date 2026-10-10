---
id: "0447"
title: 'Follow-up flag extras: optional note, remind-at/snooze, opt-in auto-clear on reply'
status: proposed
priority: 4
created: "2026-10-10"
updated: "2026-10-10"
depends_on:
    - "0441"
spec_refs: []
---

## Description
Deferred from 0441. 0441 shipped a manual boolean follow-up flag with a flagged_at timestamp. Possible extensions:
- An optional short note on the flag ("check X"), shown next to the marker.
- A remind-at/snooze time that brings the session back to attention, e.g. an iOS/web notification or moving it to the top.
- An opt-in setting to clear the flag automatically when the user sends the next message in that session.

## Acceptance criteria
- Any extension that is built extends the SetSessionFollowUp/SessionSummary contract in a backward-compatible way, and is surfaced in the clients.
- Tests cover persistence of the new fields and the reminder/auto-clear behavior.

## Work log
