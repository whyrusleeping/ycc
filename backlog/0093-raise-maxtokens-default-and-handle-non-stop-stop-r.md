---
id: "0093"
title: Raise MaxTokens default and handle non-stop stop reasons robustly
status: todo
priority: 2
created: "2026-06-30"
updated: "2026-06-30"
depends_on: []
spec_refs: []
---

## Description
## Problem

The per-turn output token cap (`config.MaxTokens`, plumbed to `engine.Loop.MaxTok` → `gollama.Options.MaxTokens`) is still too small, and the way the loop handles a budget-exhausted / non-normal stop is still confusing the model. The observed failure mode is essentially an empty tool-call / empty-content assistant message that the model then reacts to badly, even though `internal/engine/loop.go` already has a truncation-retry path (the `truncated := resp.Truncated()` branch with `truncatedStubContent` / `truncationNudge`, bounded by `maxTruncRetries`).

We should both (a) raise the default token budget and (b) handle the full range of stop reasons more deliberately so a cut-off turn never surfaces as a bare empty message.

## Scope / what to do

- Raise the default per-turn `MaxTokens`. Audit the default that flows in via:
  - `internal/config/config.go` (`Config.MaxTokens`, `DefaultAnthropic(..., maxTokens)`)
  - `internal/daemon/serve.go` (`Options.MaxTokens`)
  - the CLI default in `cmd/ycc` that seeds `Options.MaxTokens`
  Pick a sensibly larger default (and consider whether the cap should scale with the model / reasoning effort, since extended thinking eats the budget).
- Improve stop-reason handling in `internal/engine/loop.go`:
  - Inspect the actual `resp.StopReason` (and `resp.Truncated()`) rather than only branching on "no tool calls + truncated".
  - Ensure a turn cut off mid-thought (esp. when the whole budget went to a thinking block, yielding empty `msg.Content` and no tool calls) never leaves an empty/confusing assistant message in history or as a Result.Report — the sanitized stub + nudge path should reliably cover this.
  - Distinguish and react appropriately to the different stop reasons surfaced by gollama (e.g. max_tokens/length, end_turn/stop, tool_use, refusal/other) instead of treating any no-tool-call turn as a voluntary yield.
- Keep the runaway backstop (`maxTruncRetries`) but make sure the retry nudge is clear and the eventual give-up error is actionable.
- Keep replay consistency: `internal/engine/replay.go` reuses `truncatedStubContent`/`truncationNudge` to reconstruct the truncation-retry boundary — any change to that boundary must stay in sync with reopen replay.

## Acceptance criteria

- [ ] Default `MaxTokens` is raised (and the new value is consistent across config default, daemon options, and CLI default).
- [ ] The loop branches on the actual stop reason; a max-tokens/length cut-off with no tool call is handled distinctly from a normal end-of-turn yield.
- [ ] A budget-exhausted turn with empty content + no tool calls never surfaces as an empty/blank assistant message to the model or as an empty Result.Report — it is either nudged-to-continue or fails with a clear, actionable error.
- [ ] Reopen/replay (`replay.go`) still reconstructs the truncation-retry boundary correctly after the change.
- [ ] Unit tests in `internal/engine` (loop_test.go / retry_test.go / replay_test.go as appropriate) cover the new stop-reason branching and the raised default; tests pass.

## Acceptance criteria

## Work log
