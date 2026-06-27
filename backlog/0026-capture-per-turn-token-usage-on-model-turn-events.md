---
id: "0026"
title: Capture per-turn token usage on model_turn events
status: in_progress
priority: 2
created: "2026-06-26"
updated: "2026-06-26"
depends_on:
    - "0002"
spec_refs:
    - Token usage & cost accounting
    - Agent engine
    - Session & event log
---

## Description
Foundation for cost tracking (spec §20.1). The engine currently discards the `Usage`
that gollama's `Turn` already returns (`internal/engine/loop.go:175` emits `model_turn`
without it). Capture it and the model identity on every `model_turn` event so usage is
attributable per role and per model.

## Context
- gollama `ResponseMessageGenerate.Usage` exposes `PromptTokens`, `CompletionTokens`,
  `TotalTokens`, `CacheCreationInputTokens`, `CacheReadInputTokens`, and
  `PromptTokensDetails.CachedTokens` — confirmed in `/home/why/code/gollama/types.go`.
- The event `actor` already encodes the role (`coordinator`, `implementer`,
  `reviewer:<name>`), so role attribution needs no new plumbing.
- The engine `Loop` knows the resolved model id (`Loop.Model`) but NOT the logical model
  name (§13). Add a logical-name field so events can record it (e.g. "claude", "gpt").

## Acceptance criteria
- [ ] `Loop` carries the logical model name (set from the role's `AgentSpec.Name` for
      subagents and the coordinator's role name); `SetBackend` updates it on model swaps.
- [ ] `model_turn` event `data` gains `usage` (input, output, cache_read, cache_write,
      total) plus `model_name`, `backend`, and `model_id`, populated from `resp.Usage`
      and the loop's model identity.
- [ ] Zero/absent usage (backends that don't report it) records zeros without error.
- [ ] `internal/engine` (and any fake `Turner`) updated/tested so usage flows through;
      existing tests pass.
- [ ] Event rendering (`event.Render`) optionally shows a terse token count, but the
      JSONL carries the full breakdown.

## Acceptance criteria

## Work log
- 2026-06-26 plan: Capture per-turn token usage and model identity on `model_turn` events.  1. Engine `Loop` (internal/engine/loop.go): add a `ModelName` field (logical name, §13)    beside `Model` (resolved id). Set i
…[truncated]
- 2026-06-26 plan: Capture per-turn token usage + model identity on `model_turn` events.  1. Engine `Loop` (internal/engine/loop.go):    - Add a `ModelName` field (logical name per §13) alongside the existing resolved 
…[truncated]
- 2026-06-26 implementer report: Implemented per-turn token usage + model identity capture on `model_turn` events (Task 0026).  Changes: - `internal/event/event.go`: Added a typed `Usage` struct (Input/Output/CacheRead/CacheWrite/Tot
…[truncated]
- 2026-06-26 review (claude): accept — The change fully satisfies Task 0026. The engine Loop now carries logical ModelName and Backend identity (set for coordinator role names, subagent AgentSpec.Name, and the spike), SetBackend updates th
…[truncated]
