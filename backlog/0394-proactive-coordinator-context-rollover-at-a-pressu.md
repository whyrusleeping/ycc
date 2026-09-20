---
id: "0394"
title: Proactive coordinator context rollover at a pressure threshold (not only on overflow rejection)
status: proposed
priority: 2
created: "2026-09-20"
updated: "2026-09-20"
depends_on: []
spec_refs: []
---

## Description
## Problem
Coordinator rollover today is explicit (`ycc rollover`) or reactive after an actual provider context-length rejection (spec.md ~L311, internal/session/context_rollover.go). Subagents get pressure-based replacement via `exceedsContextBudget` (internal/orchestrator/orchestrator.go) but the coordinator loop never does.

Evidence: valstore session `s_bd32b8b81561efc0` (2026-09-18..20). Coordinator `gpt-6-astra` ran 585 turns with context growing monotonically 9K → 894K tokens; 357 turns had ctx > 320K. Coordinator alone billed ~96M uncached + 174M cached input tokens; the whole session (~161M uncached OpenAI input) exhausted the ChatGPT Pro weekly quota (429 `usage_limit_reached`, resets_in 556105s, at 2026-09-20T07:34Z). Because the model never rejected the request, no rollover ever fired.

## Proposal
- Trigger an owned automatic rollover (same path as overflow recovery, `beginOwnedRollover`) when `context_tokens_est >= window * safeFraction`, checked at the existing `RolloverCheckpoint` boundary.
- When the window is unknown (0), apply a conservative absolute default (e.g. 200K est. tokens) rather than disabling pressure rollover entirely; make it configurable (`context_window` / `context_safe_fraction` already exist per model).
- Emit the rollover lifecycle events with reason `pressure`; respect the existing fail-closed rules (media in view, pending questions, pause).
- Consider a session-level warning event/TUI indicator when coordinator context exceeds e.g. 50% of window.

## Acceptance
- Unit test: loop with window=W crosses W*fraction → exactly one owned rollover, context shrinks, subsequent turns continue.
- Unit test: window unknown → absolute default cap applies.
- spec.md rollover section updated to describe pressure-triggered coordinator rollover.

## Acceptance criteria

## Work log
