---
id: "0079"
title: Let coordinator preload worker agent with file/snippet context hints from the plan
status: todo
priority: 3
created: "2026-06-30"
updated: "2026-06-30"
depends_on: []
spec_refs: []
---

## Description
## Context

The worker (implementer) agent often re-does much of the same investigation the coordinator already did while writing the plan — locating the relevant files, functions, and call sites. Some independent re-investigation is intentional (we want the worker to make its own decisions and approach things its own way), but a lightweight "you'll likely want to start by looking at these files / functions" preload could cut redundant exploration and speed up the worker without dictating its solution.

## Goal

Let the coordinator attach explicit context hints — relevant file paths, code snippets, and function/symbol references — to the plan, and surface those to the worker agent as a non-prescriptive "starting points" preload when it begins work.

## Design questions / scope

- **How the coordinator emits hints**: extend `propose_plan` / the plan structure to carry an optional list of context hints (file paths, line ranges or snippets, function names) vs. a separate tool. Reuse the plan-persistence machinery (see task 0020's `## Plan` artifact).
- **Snippet vs. reference**: store actual code snippets (risk: staleness) vs. file+symbol references the worker resolves itself (fresher, cheaper). Possibly a mix.
- **How it's framed to the worker**: phrasing must be advisory ("starting points to investigate"), not "do exactly this," to preserve worker autonomy.
- **Token cost**: keep the preload small/optional so it doesn't bloat the worker's context for simple tasks.

## Acceptance criteria

- [ ] The coordinator can attach context hints (relevant files, optional snippets, function/symbol refs) to a plan.
- [ ] When a worker agent starts a task, it receives those hints as an advisory "starting points" preload (not prescriptive instructions).
- [ ] Hints are optional — tasks without them behave exactly as today.
- [ ] The preload is framed to preserve worker autonomy (suggested investigation, not mandated steps).
- [ ] Token/context cost is bounded (hints are concise; no full-file dumps unless small).
- [ ] A note in the work log / plan artifact records the hints alongside the plan (consistent with task 0020's plan persistence).

## Acceptance criteria

## Work log
