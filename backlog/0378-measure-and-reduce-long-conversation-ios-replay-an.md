---
id: "0378"
title: Measure and reduce long-conversation iOS replay and rendering costs
status: todo
priority: 2
created: "2026-09-09"
updated: "2026-09-09"
depends_on:
    - "0376"
    - "0377"
spec_refs: []
---

## Description
Split from accepted task 0343. Complete measured iOS long-conversation performance work after diagnostics and paging.

Acceptance criteria:
- Measure representative large conversations; record before/after transcript decode/replay/first-display and rendering timings, counts, and Instruments evidence.
- Address measured replay/MainActor and rendering bottlenecks; window expensive transcript rendering based on evidence.
- Preserve scroll anchoring, live streaming, question visibility, unread/routing semantics and replay correctness.
- SessionView intentionally uses eager VStack to avoid a documented LazyVStack/streaming blank-transcript geometry regression: do not blindly restore LazyVStack.
- Swift tests at useful behavioral boundaries and on-device Instruments/scroll-streaming verification on user's Mac. Do not claim source audit or Linux timings as end-to-end iOS measurements.

## Acceptance criteria

## Work log
