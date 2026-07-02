---
id: "0100"
title: 'Spec-doctor: detect spec/code drift + coverage gaps'
status: done
priority: 3
created: "2026-07-01"
updated: "2026-07-02"
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
- [x] deterministic reference-checker: extracts spec-mentioned files/symbols/packages, reports which no longer exist; unit-tested against fixture specs
- [x] pm-invocable spec-doctor flow runs pre-pass + LLM comparison and produces a report (drift, coverage gaps, stale refs)
- [x] flow can create backlog tasks for findings and propose spec edits for user approval
- [x] on-demand invocation surfaced (pm action/preset); no automatic scheduling
- [x] spec updated to document the feature; build + tests green

## Plan

Implement spec-doctor per the decided design: a pm preset/flow backed by a deterministic reference-checker helper.

1. New package `internal/specdoctor` — the deterministic pre-pass:
   - Extract candidate references from markdown docs: inline code spans only (skip fenced code blocks — they hold illustrative examples and would create false positives).
   - Classify each span conservatively:
     * path-like (contains '/' or ends in a known extension like .go/.md/.toml/.proto): check existence with os.Stat relative to the workspace root (file OR directory both OK — this covers package dirs like `internal/docs`). Skip spans containing glob metacharacters (*?[) — they are patterns, not refs.
     * symbol-like (Go-ish identifier: CamelCase, snake_case, or dotted `Type.Method`): resolved by a word-boundary search across workspace source files, excluding .git/vendor/node_modules and the docs set itself plus backlog/ (so a ref never trivially matches its own mention). Found anywhere → OK; found nowhere → stale.
     * anything ambiguous → skipped, not flagged (zero-false-positive discipline).
   - `Check(root string, docs []DocFile) *Report` returning found/missing refs with doc + line, and a `Report.Markdown()` renderer (counts, stale refs grouped by doc/line, "all N references resolve" when clean).
   - Unit tests against fixture spec content + a temp workspace tree (existing/missing files, dirs, symbols, fenced-block skipping, glob skipping).

2. `internal/docs`: add `Store.DocFiles() ([]string, error)` — enumerates the docs set: the spec entry point (if it exists) plus workspace files matching the configured `doc_globs` (walk the workspace, match workspace-relative slash paths with the existing matchGlob; skip .git). Unit test.

3. Orchestrator: new `spec_check` tool (no params) that runs the checker over the docs set and returns the markdown report; register it in the pm mode registry in BuildMode.

4. New `spec-doctor` preset in Presets() (mode "pm") whose prompt drives the two-phase flow:
   - Phase 1: call spec_check; the deterministic result seeds and grounds phase 2.
   - Phase 2: LLM comparison — walk the spec sections, read the relevant code, and flag only DRIFT (spec states behavior the code contradicts) and COVERAGE GAPS (significant internal/* packages, RPCs, or tools with no spec section). The spec is intentionally higher-level than code: flag contradictions only, never missing detail.
   - Output: present a consolidated report to the user; offer to create backlog tasks per actionable finding (create_task) and draft spec edits, applying edits only with user approval (ask_user). On-demand only; no scheduling.

5. Spec: document the feature — a new subsection (e.g. §6.4 "Spec doctor — drift & coverage checking") describing the two-phase shape, output (report + proposed tasks + suggested edits), on-demand cadence, and the false-positive discipline; update §9's "sole remaining preset is onboard" wording to include spec-doctor; add internal/specdoctor to §15 package layout if listed.

6. Verify: go build ./... && go test ./... green; extend modes_test for the pm registry containing spec_check and the new preset.

### Starting points
- internal/orchestrator/modes.go — BuildMode (pm registry at ~line 87), Presets()/Preset struct, tool definition pattern (createTask, switchToWork)
- internal/orchestrator/orchestrator.go — listPlans/runPlan (~line 317) as simple no-param-ish tool examples; Deps struct
- internal/docs/spec.go — SpecPath/ReadSpec/IsDoc; internal/docs/config.go — specConfig.DocGlobs, matchGlob (reuse for DocFiles)
- internal/orchestrator/modes_test.go — existing preset/registry tests to extend
- spec.md §6.1 (docs set), §9 (modes/presets, mentions onboard as sole preset), §15 (package layout)
- tools helpers: tools.Obj / tools.StrProp / tools.OkResult / tools.ErrResult in internal/tools

## Work log
- 2026-07-05 design pass with user (pm session): shape/output/cadence decided (see
  description) — pm preset/flow + deterministic reference-checker; output = report +
  proposed tasks + suggested spec edits; on-demand only; no upfront cost bounding.
  Unblocked for implementation.
- 2026-07-02 blocked: parked for the overnight autonomous work-loop run — this task required an interactive design pass with the user before any implementation. That pass happened 2026-07-05.
- 2026-07-02 plan: Implement spec-doctor per the decided design: a pm preset/flow backed by a deterministic reference-checker helper.  1. New package `internal/specdoctor` — the deterministic pre-pass:    - Extract ca
…[truncated]
- 2026-07-02 context hints: 6 recorded with plan
- 2026-07-02 context hints: internal/orchestrator/modes.go — BuildMode (pm registry ~line 87), Presets()/Preset struct, tool patterns (createTask, switchToWork); internal/orchestrator/orchestrator.go — listPlans/runPlan (~li
…[truncated]
- 2026-07-02 implementer report: Implemented spec-doctor (task 0100): a pm preset/flow backed by a deterministic reference-checker.  ## What changed  **New package `internal/specdoctor`** (`specdoctor.go` + `specdoctor_test.go`) — 
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The change implements spec-doctor exactly as designed: a new `internal/specdoctor` deterministic reference-checker (paths/dirs via os.Stat, symbols via word-boundary corpus search, inline-code-span-on
…[truncated]
- 2026-07-02 decision: accept — commit: Spec-doctor: deterministic reference check + pm drift/coverage flow (task 0100)  - internal/specdoctor: extracts inline-code-span refs (paths, package dirs,   Go-ish symbols) from the docs set and ver
…[truncated]
- 2026-07-02 usage: 73,630 tok (in 252, out 73,378, cache_r 7,767,539, cache_w 248,286) · cost n/a (unpriced)
  implementer: 57,267 tok (in 188, out 57,079, cache_r 6,618,871, cache_w 102,834) · cost n/a (unpriced)
  coordinator: 11,663 tok (in 36, out 11,627, cache_r 829,844, cache_w 108,174) · cost n/a (unpriced)
  reviewer:Claude: 4,700 tok (in 28, out 4,672, cache_r 318,824, cache_w 37,278) · cost n/a (unpriced)
