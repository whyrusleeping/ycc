---
id: "0534"
title: Work sessions see the project's other live sessions and trust it when picking a task
status: done
priority: 2
created: "2026-11-10"
updated: "2026-11-10"
depends_on: []
spec_refs:
    - spec.md#9.1 Unattended work loop
---

## Description
## Why
On 2026-11-10 a valstore work-loop session skipped two ready p2 tasks for a p3 because it guessed from git commit times and `pgrep` that another session was working on them. In fact no other session was running: the user's chat session that made those commits had gone idle. The daemon already knows which sessions are live and what they hold, but work sessions are never shown this or told it is authoritative.

## Acceptance criteria
- In work sessions, `list_backlog` output lists the project's other live sessions (mode, state such as running/idle/waiting, focused tasks, running jobs) or says no other session is live, from daemon state (reuse the watch activity snapshot logic, excluding the session itself).
- The work prompt says this list and the READY/held markers are authoritative, so the agent does not infer parallel work from commit times, file mtimes or processes, and treats an in_progress task that no running session holds as continuation work.
- spec.md §9.1 records the behavior.
- A test covers an idle session that appears as idle and holds nothing, and a running one that shows its focused task.

## Work log
Outcome: `list_backlog` ends with the project's other live sessions from daemon state (shared with the watch activity snapshot; "working on" vs "idle since … earlier focus … (not held)"), the work prompt says to trust it rather than commit times or processes, spec §9.1 updated, TestOtherSessionsNoteSeparatesWorkingFromIdle. Commit: "work sessions: list the project's other live sessions in list_backlog (0534)".
