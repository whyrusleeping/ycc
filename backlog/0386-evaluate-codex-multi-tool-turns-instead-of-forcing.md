---
id: "0386"
title: Evaluate Codex multi-tool turns instead of forcing one call per request
status: done
priority: 2
created: "2026-09-17"
updated: "2026-09-17"
depends_on: []
spec_refs:
    - 7.1 Provider boundary and history
    - 7.3 Subagents and asynchronous jobs
---

## Description
Vals retrospective finding: before 2026-09-17 UTC, all 39,118 non-synthetic astra/sol tool-using turns contained exactly one call. Current internal/codex/codex.go buildRequest sets ParallelToolCalls:false even though prompts encourage batching; engine already dispatches batches serially. Investigate permitting multi-call generation without introducing concurrent mutation. This is a measured evaluation proposal, not approval to flip the production default.

Acceptance criteria:
- Establish provider compatibility and document why false was originally chosen; test enabled/disabled request variants against the actual Codex backend with explicit bounded live-run consent.
- Extend existing behavioral evaluations to independent reads/searches, dependent edits, control-stop batches, interruption/reopen and tool-result pairing.
- Keep dispatch ordering, mutation ownership, fail-stop logging, and correct replay/provider item ordering intact.
- Compare correctness first, then real API round trips, input/cache/output classes, elapsed time and result volume on repeated matched scenarios. Exclude synthetic preload turns.
- Adopt a default only if evidence supports it; preserve a compatibility escape hatch if needed.

Evidence and context: docs/reports/vals-harness-retrospective.md; related existing evaluation task 0360 and analytics proposal 0320.

## Acceptance criteria

## Work log

- 2026-09-17: User explicitly superseded the evaluation scope: enable parallel tool calls directly by flipping the boolean; no study requested. Changed `buildRequest` to send `ParallelToolCalls: true`. Tool dispatch remains sequential and otherwise unchanged. `go test ./internal/codex` and `git diff --check` passed. Closing against this revised user-approved scope, not claiming the originally proposed study was performed.
