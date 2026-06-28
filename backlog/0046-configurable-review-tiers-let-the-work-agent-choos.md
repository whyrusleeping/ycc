---
id: "0046"
title: Configurable review tiers — let the work agent choose review intensity per change
status: done
priority: 3
created: "2026-06-27"
updated: "2026-06-27"
depends_on: []
spec_refs: []
---

## Description
## Description

Today the `work` orchestration applies a single, fixed review approach (the current
reviewer default — a single Opus review). Reviews should be tiered, and the coordinator
should decide how much review a given change warrants based on its size/risk.

Introduce a configurable set of named **review types** (tiers), and let the work agent
select one per change:

1. **simple** — no separate reviewer; the coordinator reviews the change itself. Intended
   only for smaller / low-risk changes.
2. **single opus review** — the current default: one Opus reviewer.
3. **high powered** — Opus *and* GPT-5.5 running parallel reviews, results combined.

The set of review types should be configurable (not hard-coded), so tiers and the
models/agents backing them can be added or changed. The coordinator picks a tier per
change; how aggressively it should default is a design question (see acceptance criteria).

## Acceptance criteria

- [ ] review types are defined as named, configurable tiers (at minimum: `simple`,
      `single-opus`, `high-powered`), each mapping to a review strategy + model/agent set
- [ ] `simple` tier = coordinator self-reviews, no separate reviewer agent spawned
- [ ] `single-opus` reproduces the current default reviewer behavior
- [ ] `high-powered` runs Opus and GPT-5.5 reviewers in parallel and aggregates their findings
- [ ] the work/coordinator agent selects a review tier per change (size/risk-based), with a
      sensible default; criteria/heuristic documented
- [ ] tier selection is surfaced in the work log / events so it's auditable
- [ ] config schema for review types documented; unknown/missing tier degrades gracefully

## Notes
- Relates to the reviewer default and the `work` orchestration; coordinate model ids with the
  multi-backend model work (0041/0042).
- Parallel high-powered reviews interact with reviewer sandboxing (0008).

## Acceptance criteria

## Work log
- 2026-06-27 plan: Add configurable review tiers that the work-coordinator selects per change.  CONFIG (internal/config/config.go): - New types: `ReviewTier{ Strategy string `toml:"strategy"`; Models []string `toml:"mod
…[truncated]
- 2026-06-27 implementer report: Implemented configurable, tiered code review that the work-coordinator selects per change.  ## Changes **internal/config/config.go** - New `ReviewTier{Strategy, Models}` and `Reviews{Default, Tiers}` 
…[truncated]
- 2026-06-27 review (claude): accept — The change delivers configurable, named review tiers (simple/single-opus/high-powered) with a TOML schema, validation, graceful degradation, per-change selection via the spawn_reviewers review_tier pa
…[truncated]
- 2026-06-27 revision: Addressed the review's doc-accuracy fix (docs only, no behavior change):  1. **spec.md §13.1** — extended the `high-powered` bullet to clarify that out of the box the built-in tier resolves to the 
…[truncated]
- 2026-06-27 review (claude): accept — The revision resolves my earlier minor finding: the spec (§13.1) and the spawn_reviewers tool description now accurately state that the built-in high-powered tier resolves to the same reviewer set as
…[truncated]
- 2026-06-27 decision: accept — commit a066d98: Configurable review tiers: coordinator selects review intensity per change (§13.1)  Add named, configurable review tiers (simple/single-opus/high-powered) via a [reviews] TOML schema. The work-coordi
…[truncated]
- 2026-06-27 usage: 56,097 tok (in 186, out 55,911, cache_r 4,802,657, cache_w 195,033) · cost n/a (unpriced)
