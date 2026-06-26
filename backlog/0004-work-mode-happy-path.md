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

## Work log
- 2026-06-26 implemented:
  - `internal/docs`: structured backlog — Task (YAML frontmatter round-trip), Store with
    List/Get/Create/Update/AppendWorkLog/RenderIndex, next-id + slug. Tests pass.
  - `internal/git`: thin git wrapper — Open (init + .gitignore .ycc/ + initial commit),
    Diff (staged), Commit (short sha).
  - `internal/tools`: `Reviewer` set (read/inspect + bash) + `submit_review` control tool;
    exported tool-building helpers (Obj/StrProp/GetString/ErrResult/OkResult, Finish).
  - `internal/orchestrator`: coordinator system prompt + tools list_backlog/get_task/
    propose_plan/spawn_implementer/spawn_reviewer/commit/update_task/finish. Spawn tools
    build child engine.Loops (implementer = Worker tools; reviewer = Reviewer tools) that
    emit into the same session log under distinct actors; review JSON parsed with a
    plain-text fallback.
  - `internal/event`: REFACTOR — seq authority moved from Emitter to the Recorder (Log),
    so coordinator + subagents share one monotonic sequence. Added event types
    subagent_spawned/finished, plan_proposed, review_submitted, decision_made,
    doc_updated, commit_made. `Emitter.With(actor)` for subagents.
  - `internal/session`: Manager builds the coordinator (mode "work") vs a worker agent.
- 2026-06-26 verified LIVE (claude-opus-4-8): seeded a 1-task backlog; one `ycc start`
  drove the full loop — coordinator picked 0001, planned, implementer wrote a go module +
  test and ran `go test`, reviewer ran git diff/test/vet and accepted, coordinator
  committed (sha 30fcb25) and marked the task done. Work log captured plan/report/review/
  decision+sha; backlog.md regenerated; .ycc/ kept out of git. All criteria pass.
- Notes: M2 is the happy path (N=1, no revise loop). Subagents run sequentially and reuse
  the same Anthropic backend; multi-model + revise loop + interaction levels are 0005.