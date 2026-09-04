---
id: "0341"
title: 'iOS: hub-and-spoke navigation — cross-link jumps replace the stack'
status: in_review
priority: 3
created: "2026-08-17"
updated: "2026-08-17"
depends_on: []
spec_refs: []
---

## Description
User complaint: too many Back taps to get home, and each pop felt like an RTT (stale intermediate screens refreshing as they reappear).

Change: `HomeRouter.open` no longer pushes with screen dedupe. New semantics:
- Cross-link jumps (session → backlog, task → session, drawer → anything) **replace** the whole stack with the destination, so one Back always returns to Recent.
- `NavigationLink` drill-ins (Recent → session, backlog → task) still push normally.
- Lateral same-kind moves (task → dependency task) swap the top in place, keeping the parent (backlog) beneath.
- Parameter merging (title/live) preserved; unchanged re-opens don't rebuild.

Files: clients/ios/App/HomeRouter.swift (core), comment updates in LandingView/SessionView/TaskDetailView, docs/design/ios-client.md wording.

Acceptance: builds on Mac; on device, Back from any cross-linked screen lands on Recent in one tap; backlog → task → Back returns to backlog; task dependency hops don't grow the stack.

## Acceptance criteria

## Work log
