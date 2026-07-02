---
id: "0100"
title: 'Spec-doctor: detect spec/code drift + coverage gaps (design with user first)'
status: blocked
priority: 3
created: "2026-07-01"
updated: "2026-07-02"
depends_on: []
spec_refs: []
---

## Description
## Description

**This task needs a design pass WITH THE USER before any implementation — do not start building until the shape is agreed. Open the conversation with concrete options and trade-offs, then update this task with the decided design before writing code.**

The spec's founding principle is "the durable state of a project lives in documents" and "**a drifted spec is a bug**" (§1). Today that is enforced only by *prompting* the coordinator to keep the spec true — nothing actively detects when `spec.md` and the actual code have diverged, or where the spec has gaps. A **spec-doctor** capability would lean into the docs-driven premise instead of merely asserting it, and is a genuinely novel/defensible feature.

Rough intent (to be refined in design): a mode/flow/runbook that inspects the codebase against `spec.md` and surfaces:
- **Drift** — spec sections that describe behavior the code no longer matches (renamed/removed components, changed architecture, stale RPCs/tools).
- **Coverage gaps** — significant code areas (e.g. `internal/*` packages, exported RPCs, tools) with no corresponding spec section.
- **Stale references** — spec mentions of files/symbols/sections that no longer exist.

Design questions to resolve with the user first:
- Is this a new **mode**, a **pm preset**, or a reusable **plan/runbook** (§6.3)? (Leaning: pm-adjacent, since it reads/edits docs and does no code changes.)
- What's the **output** — a report event? proposed `update_spec` edits for approval? new backlog tasks for each drift item? a "spec coverage" score surfaced on the home dashboard?
- How is drift **detected** — pure-LLM read-and-compare over sampled sections, deterministic heuristics (grep spec for symbol/section names and check existence), or a hybrid? How do we bound cost on a large repo (§20)?
- **Cadence** — on demand, on a schedule, or triggered after N commits / at the end of a work-loop?
- How to avoid **false positives** (the spec is intentionally higher-level than code) so users trust it.

Relevant code / prior art:
- `internal/orchestrator/prompts.go`, `modes.go` — mode/preset wiring and pm tool set.
- `internal/docs` — spec section read/write (`update_spec`), backlog store.
- `plans/*.md` + `list_plans`/`run_plan`/`save_plan` if it lands as a runbook.

## Acceptance criteria
- A design discussion with the user has happened and this task's description is updated with the agreed shape (mode vs preset vs runbook, detection strategy, output form, cadence, cost bounds) BEFORE implementation.
- (Post-design) implementation matches the agreed design; spec §updates document the feature; build + tests green.

## Acceptance criteria

## Work log
- 2026-07-02 blocked: parked for the overnight autonomous work-loop run — this task requires an interactive design pass with the user before any implementation. Unblock by holding that design discussion (pm session) and recording the agreed shape here.
