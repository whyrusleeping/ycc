---
id: "0101"
title: 'Home menu: notify when tasks are blocked and waiting on the user'
status: todo
priority: 2
created: "2026-07-01"
updated: "2026-07-01"
depends_on: []
spec_refs: []
---

## Description
## Description

When a task gets blocked (an autonomous/loop session marks it `blocked` because it needs user input — §10/§11), that fact is passive: the loop silently skips it and the reason lives only in the task's work log. A user returning to ycc has no signal from the **home menu** that the project is waiting on them.

Surface **blocked-on-you** items on the home menu so returning users immediately see "the agent needs you." The menu already fetches modes/presets; add a blocked-task indicator alongside it.

Scope:
- Home menu (`internal/tui/tui.go`, `stateMenu` / menu view, `modesMsg`) shows a count/badge of blocked tasks (e.g. "⚠ 2 tasks blocked — waiting on you"), or nothing when zero.
- Selecting/activating it jumps to those tasks (reuse the backlog browser, filtered to `blocked`), where the user can read why each is blocked and unblock it (ties into the batch digest task 0098 and backlog grooming task 0099).
- Data comes from the existing `ListBacklog` projection (each summary already carries status); no new RPC strictly required, though a `blocked`-with-reason field may be worth adding if the reason isn't already easily surfaced.

Keep it lightweight and non-nagging: a single glanceable indicator, not a modal that interrupts.

## Acceptance criteria
- The home menu shows a blocked-task indicator with a count when one or more tasks are `blocked`, and shows nothing when none are.
- Activating the indicator routes to the blocked tasks (filtered backlog view) where the block reason is visible.
- The indicator reflects current state on menu (re)entry (e.g. after a loop run leaves a task blocked).
- Build + tests green; a test covers the indicator appearing/hiding based on backlog state.

## Acceptance criteria

## Work log
