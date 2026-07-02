---
id: "0100"
title: 'Spec-doctor: detect spec/code drift + coverage gaps'
status: todo
priority: 3
created: "2026-07-01"
updated: "2026-07-05"
depends_on: []
spec_refs: []
---

## Description

The spec's founding principle is "the durable state of a project lives in documents" and
"**a drifted spec is a bug**" (§1). Today that is enforced only by *prompting* the
coordinator to keep the spec true — nothing actively detects when `spec.md` and the
actual code have diverged, or where the spec has gaps.

**Design decided with the user (2026-07-05 pm session):**

- **Shape:** a **pm preset/flow backed by a deterministic reference-checker helper**
  (not a bare runbook, not a dedicated mode). Two phases:
  1. *Deterministic pre-pass (code):* a helper tool extracts file paths, symbols, and
     package names mentioned in `spec.md` and checks they exist in the repo. Stale
     references are found mechanically with zero false positives, and the result seeds
     the LLM phase.
  2. *LLM pass:* compares spec sections against the relevant code (grounded by the
     pre-pass output) to find **drift** (spec describes behavior the code no longer
     matches) and **coverage gaps** (significant `internal/*` packages, RPCs, tools
     with no spec section).
- **Output:** all three — a drift/coverage **report**, **proposed backlog tasks** per
  actionable finding, and **suggested spec edits** for user approval (pm already has
  create_task + spec-edit tools; the flow drafts, the user approves).
- **Cadence:** on-demand only (a pm action). No scheduling/auto-trigger for now.
- **Cost bounding:** not designed up front — start unbounded, observe real cost, add
  sampling or git-driven targeting later if needed.
- False-positive discipline: the spec is intentionally higher-level than code; the LLM
  pass should flag only contradictions, not missing detail.

Relevant code / prior art:
- `internal/orchestrator/prompts.go`, `modes.go` — mode/preset wiring and pm tool set.
- `internal/docs` — spec section read/write (`update_spec`), backlog store.

## Acceptance criteria
- [ ] deterministic reference-checker: extracts spec-mentioned files/symbols/packages, reports which no longer exist; unit-tested against fixture specs
- [ ] pm-invocable spec-doctor flow runs pre-pass + LLM comparison and produces a report (drift, coverage gaps, stale refs)
- [ ] flow can create backlog tasks for findings and propose spec edits for user approval
- [ ] on-demand invocation surfaced (pm action/preset); no automatic scheduling
- [ ] spec updated to document the feature; build + tests green

## Work log
- 2026-07-05 design pass with user (pm session): shape/output/cadence decided (see
  description) — pm preset/flow + deterministic reference-checker; output = report +
  proposed tasks + suggested spec edits; on-demand only; no upfront cost bounding.
  Unblocked for implementation.
- 2026-07-02 blocked: parked for the overnight autonomous work-loop run — this task required an interactive design pass with the user before any implementation. That pass happened 2026-07-05.
