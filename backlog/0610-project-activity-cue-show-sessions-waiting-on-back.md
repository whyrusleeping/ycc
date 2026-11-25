---
id: "0610"
title: 'Project activity cue: show sessions waiting on background jobs apart from working ones (iOS chip squares, web badge)'
status: proposed
priority: 4
created: "2026-11-25"
updated: "2026-11-25"
depends_on: []
spec_refs:
    - spec.md
---

## Description
Status: proposed (the user asked on 2026-11-25 to file it as proposed; it is not yet accepted).

**Problem.** A project's activity count counts every live session that is running, paused, or idle with `awaiting_jobs`: iOS `SessionListModel.accumulate` draws one square per session on the ScopeChipBar chip and the drawer badge, and web `features/sessions/activity.ts` drives the ProjectPicker badge the same way. The work loop view shows only its `parallel` working slots, and waiting loop sessions do not count against those (spec "work loop": waiting sessions do not count against the working slots; `[work_loop] max_waiting`). On 2026-11-25 the user saw 7 squares on the wheeler chip but only 4 active sessions in the loop view and asked whether something was broken. Nothing was broken. Wheeler runs `parallel = 4` with `max_waiting = 8`. Between about 18:50 and 20:15 UTC on 2026-11-24, its live sessions were the 4 working loop sessions, loop sessions waiting on long shell jobs, and a 0342 (historical link scan) session from an earlier loop, 7 in all.

**Outcome.** The activity cue separates sessions that are working from sessions that are idle and only waiting on their own background jobs (`SessionSummary.awaiting_jobs`). For example, iOS could draw hollow squares for waiting sessions, and web could split the badge or its title into "N working · M waiting". The accessibility label and the web title say "working" and "waiting on jobs" instead of a single "active".

**Acceptance criteria**
- `ProjectActivity` (iOS) and the web counts keep a separate waiting count. Idle + `awaiting_jobs` goes to waiting; running and paused go to working. The total stays the same as today's `active`.
- The iOS chip squares, the drawer badge and the closed-menu cue, and the web ProjectPicker badge show the two counts in distinct styles. A project with only waiting sessions doesn't look the same as one with working sessions.
- YccKit unit tests and web tests cover the split, including a session with `awaiting_jobs` true while running (it counts as working).
- iOS is built and screenshotted on the configured Mac per project practice.

**Not in scope.** Telling a session blocked inside a long `wait` tool call (status `running`) apart from one that is doing work. The 10-minute loop wait cap (commit 0d19031) already limits that case.

## Acceptance criteria

## Work log
