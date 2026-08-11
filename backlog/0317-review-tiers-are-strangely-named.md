---
id: "0317"
title: Review tiers are strangely named
status: done
priority: 3
created: "2026-08-11"
updated: "2026-08-11"
depends_on: []
spec_refs: []
---

## Description
“Single opus” was an example i gave the agent initially, not a proscription. The main idea is that there are different tiers with varrying numbers of models doing review with potentially different prompts

## Acceptance criteria

- Effective tier listings expose only the canonical built-ins `self-review`, `standard`, and `comprehensive`, with `standard` as the default.
- `self-review` spawns no reviewer agent, `standard` uses the first configured reviewer, and `comprehensive` uses every configured reviewer.
- The legacy names `simple`, `single-opus`, and `high-powered` resolve to their corresponding canonical tiers, including legacy configured overrides and defaults, without appearing in effective listings.
- Runtime review-tier mutations accept legacy aliases but persist canonical built-in names; custom tiers, reviewer focus prompts, and reasoning overrides continue to work.
- Coordinator guidance, RPC/protocol documentation, the spec, and iOS settings copy use the canonical names.
- Focused Go and Swift tests cover canonical behavior and compatibility aliases; relevant Go tests and repository-wide Go tests pass, and Swift tests are run where the toolchain is available.

## Plan

Replace the model-specific/vague built-in review-tier names with behavior- and intensity-oriented names: `self-review`, `standard` (the default), and `comprehensive`. Make the built-ins meaningfully differ when multiple reviewer roles are configured: self-review spawns none, standard uses one reviewer, and comprehensive fans out to all configured reviewers; configured tier overrides still support arbitrary reviewer sets and focus prompts. Preserve existing installations by accepting the legacy names `simple`, `single-opus`, and `high-powered` as aliases that resolve to the corresponding canonical tiers (including legacy defaults/overrides), while listing and persisting canonical names for new operations. Update coordinator-facing descriptions, config/RPC behavior, iOS text, protocol comments/generated artifacts, spec documentation, and focused Go/Swift tests. Add explicit acceptance criteria to the task, run relevant Go tests plus repository-wide Go tests, and run available Swift tests or document the environment limitation.

### Starting points
- internal/config/config.go: built-in tier definitions/effectiveReviewTiers/default resolution and mutation APIs
- internal/orchestrator/orchestrator.go: fallback tool blurb and review_tier schema examples
- spec.md §13.1; proto/ycc/v1/ycc.proto review-tier comments; clients/ios/App/ReviewTiersView.swift
- The worktree already contains unrelated backlog bookkeeping; do not overwrite or include those changes.

## Work log
- 2026-08-11 plan: Replace the model-specific/vague built-in review-tier names with behavior- and intensity-oriented names: `self-review`, `standard` (the default), and `comprehensive`. Make the built-ins meaningfully d
…[truncated]
- 2026-08-11 context hints: 4 recorded with plan
- 2026-08-11 context hints: Keep legacy aliases hidden from effective tier listings/tool descriptions; canonicalize runtime mutation inputs so new persisted config uses canonical names.; The worktree already contains unrelated b
…[truncated]
- 2026-08-11 preload: 6 file(s), ~25 KiB seeded into implementer context
- 2026-08-11 implementer report: Implemented task 0317.  Changed: - Replaced built-in review tiers with canonical `self-review`, `standard` (default), and `comprehensive` names. - Made built-in behavior distinct: no agents, first con
…[truncated]
- 2026-08-11 review tier: high-powered — reviewers: sol, claude
- 2026-08-11 review (sol): accept — The change correctly introduces the canonical self-review/standard/comprehensive built-ins, gives them the required zero/one/all reviewer behavior, preserves legacy aliases and overrides without leaki
…[truncated]
- 2026-08-11 review (claude): accept — The change cleanly renames the built-in review tiers to self-review/standard/comprehensive with standard as default, makes the built-ins behaviorally distinct (no reviewers / first reviewer / all revi
…[truncated]
