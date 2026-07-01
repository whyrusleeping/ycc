---
id: "0107"
title: 'Home menu: surface live sessions that are waiting for the user'
status: todo
priority: 3
created: "2026-07-01"
updated: "2026-07-01"
depends_on: []
spec_refs:
    - 18.6 Session history browser & reopen
    - 9. Modes (the home menu)
---

## Description
## Description
The home menu shows blocked *tasks* ("⚠ N tasks blocked — waiting on you") but says nothing about *sessions*: one sitting on an unanswered ask_user, paused mid-steer, or still running keeps going invisibly (persistent daemon) unless the user remembers ctrl+r. Add an awareness line, e.g. `⚠ 1 session waiting for your answer — press s to open`, sourced from the durable session index / live manager (status running/paused + pending question flag). Selecting it should jump straight into that session (reopen/attach), not just the list.

## Acceptance criteria
- [ ] Menu shows a line when any live session has a pending question or is paused; count + shortest route in
- [ ] The key/entry attaches directly to that session (picker when several)
- [ ] No line when nothing needs the user
- [ ] Works against both one-shot and persistent daemons (ListSessionHistory/ListSessions already carry status)

## Acceptance criteria

## Work log
