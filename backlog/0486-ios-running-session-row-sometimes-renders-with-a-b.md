---
id: "0486"
title: 'iOS: running session row sometimes renders with a blank background'
status: in_review
priority: 3
created: "2026-10-29"
updated: "2026-10-29"
depends_on: []
spec_refs: []
---

## Description
On device (seen twice, 2026-10-28), the first row of a session-list section was running but drew no background at all: no card fill and no glow. It showed up after returning to the list from a session view (after app resume / pressing back). Could not reproduce in the iOS 27 simulator, including with added latency, backgrounding, locking, and moves or inserts.

Mitigation in LandingView.swift: RunningRowBackground draws the card fill outside the TimelineView, and its glow is rebuilt (`.id(epoch)`) whenever the list comes back into view (path back to root, scenePhase active).

Acceptance: on-device use shows no more blank running rows. If they come back, move the glow into the row content instead (keeping the system background preserves the press highlight).

## Acceptance criteria

## Work log
