---
id: "0004"
title: work mode happy path (M2)
status: done
priority: 2
created: 2026-06-25
updated: 2026-06-25
depends_on: ["0003"]
spec_refs: ["The work orchestration", "Document model", "Tools"]
---

## Description
M2 is the `work` happy-path milestone in the original spec roadmap (spec.md at 7365ee4; current spec.md no longer lists milestones).

The core loop end-to-end with N=1 and no revise round: coordinator reads the structured
backlog, picks/accepts a task, proposes a plan, spawns an implementer, spawns a single
reviewer, and on acceptance commits + marks the task done + appends the work log.

## Acceptance criteria
- [x] docs package: parse/write task files, validate frontmatter, render backlog.md,
      append work log, list/get/create/update (backlog.md rendering was later removed by
      0123, which stopped generating the duplicate index)
- [x] coordinator tools: list_backlog, get_task, propose_plan, spawn_implementer,
      spawn_reviewer, commit, update_task, finish
- [x] implementer subagent returns a structured report + staged diff
- [x] one reviewer returns structured findings; coordinator accepts and commits
- [x] task file work log records plan / report / review / decision / commit sha

## Outcome

Implemented the structured backlog store, git wrapper, coordinator tools, implementer/reviewer subagents, shared event sequencing, and work-mode session assembly. A live Claude run completed the full pick-plan-implement-test-review-commit flow and marked the seeded task done.

Commit: 7365ee4 — ycc: initialize workspace

## Work log
- 2026-11-14 groom: glossed M2, checked off the criteria on this done task as the Outcome reports, and noted that 0123 later removed backlog.md rendering (7365ee4's internal/docs had `RenderIndex`).
