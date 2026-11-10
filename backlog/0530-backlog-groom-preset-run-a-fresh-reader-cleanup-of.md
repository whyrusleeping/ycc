---
id: "0530"
title: 'backlog-groom preset: run a fresh-reader cleanup of a project''s backlog'
status: todo
priority: 2
created: "2026-11-10"
updated: "2026-11-10"
depends_on:
    - "0528"
spec_refs:
    - docs/design/doc-style.md
---

## Description
## Why
The user wants a ready-made prompt they can run in any project (user request, 2026-11-10, this session) to clean up its backlog against the backlog section of `docs/design/doc-style.md` (task 0528). ycc already ships maintenance presets: onboard, spec-doctor and memory-groom in `internal/orchestrator/modes.go`. This adds a `backlog-groom` preset next to them. Task 0521 is moving maintenance presets off the new-session screen; place this one wherever presets end up.

## Behaviour
The pm session reads the project's backlog as a fresh reader, preferably on a different model from the usual task authors (preset model binding, like memory-groom). For each problem it finds, it fixes it or flags it:
- **Debris (fix):** `…[truncated]` fragments that carry no information, duplicate or empty section headings, stray template text, invalid UTF-8.
- **Stale or contradictory state (fix when evidence settles it, otherwise flag):** a Description that contradicts `status`, unchecked criteria on done tasks, a "current state" that's out of date. Evidence means git history, the code, session events, and linked bench or docs files.
- **Insider references (fix):** add short glosses to bare task ids and codenames, and point references to deleted tasks at what replaced them.
- **Inherited authority and unsourced user claims (flag, never invent):** "slice of accepted N" chains, repeated authorization disclaimers, and "owner direction" with no source. Find the source in session events if one exists; otherwise mark the claim as the agent's inference. Never manufacture a decision.
- **Degenerate text (rewrite from evidence):** e.g. dropped spaces between words. Re-derive from the recorded evidence instead of paraphrasing the damaged text.
- **Near-duplicates and runaway logs (flag, propose):** duplicate tasks get a proposed merge. Huge work logs get a current-state summary at the top; history isn't deleted.

It edits only backlog files. It changes task meaning or status only when evidence settles it, and records each change in the task's work log ("groom: …"). It ends with a short report: what it changed, and a list of what it flagged for the user, with ids. It works in a bounded batch, oldest unreviewed tasks first, so it can run on large backlogs (valstore has about 560 tasks).

Known first targets in other projects:
- valstore 0296, 0297 and 0248: Sep 19–21 work-log entries with dropped spaces ("notanautomaticretry"); 0248 is marked done but its Description says "blocked".
- wheeler: 71 tasks hanging off a missing 0068.

## Acceptance criteria
- A `backlog-groom` preset exists with title, description and prompt, startable by name from the web and iOS clients like the other presets.
- The prompt implements the behaviour above, references doc-style.md, and edits only backlog files.
- The spec lists the preset next to spec-doctor and memory-groom.
- A run against a copy of valstore's backlog leaves 0296 readable and 0248's status consistent, and produces a flag list; record the result in the work log.

## Work log
