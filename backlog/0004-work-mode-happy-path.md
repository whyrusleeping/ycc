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
The core loop end-to-end with N=1 and no revise round: coordinator reads the structured
backlog, picks/accepts a task, proposes a plan, spawns an implementer, spawns a single
reviewer, and on acceptance commits + marks the task done + appends the work log.

## Acceptance criteria
- [ ] docs package: parse/write task files, validate frontmatter, render backlog.md,
      append work log, list/get/create/update
- [ ] coordinator tools: list_backlog, get_task, propose_plan, spawn_implementer,
      spawn_reviewer, commit, update_task, finish
- [ ] implementer subagent returns a structured report + staged diff
- [ ] one reviewer returns structured findings; coordinator accepts and commits
- [ ] task file work log records plan / report / review / decision / commit sha

## Outcome

Implemented the structured backlog store, git wrapper, coordinator tools, implementer/reviewer subagents, shared event sequencing, and work-mode session assembly. A live Claude run completed the full pick-plan-implement-test-review-commit flow and marked the seeded task done.

Commit: 7365ee4 — ycc: initialize workspace