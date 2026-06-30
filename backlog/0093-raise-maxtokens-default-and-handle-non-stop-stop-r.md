---
id: "0093"
title: Raise MaxTokens default and handle non-stop stop reasons robustly
status: done
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

## Plan

Two parts.

PART A — raise default MaxTokens to a single shared constant.
- Add `const DefaultMaxTokens = 32000` to internal/config/config.go (documented: per-turn output cap; larger so extended-thinking budgets aren't exhausted mid-turn; default model Opus 4 supports it).
- Replace the three production literals `8192` with config.DefaultMaxTokens: cmd/ycc/main.go flag `max-tokens` Value (~540); cmd/ycc/main.go in-process daemon fallback (~604); internal/setup/setup.go generated config (~125).
- daemon/serve.go already plumbs the supplied value through DefaultAnthropic — no literal there.
- Leave *_test.go call sites that pass 8192 explicitly; add one assertion that DefaultMaxTokens is the raised value.

PART B — robust stop-reason handling in internal/engine/loop.go.
- After `msg := resp.Choices[0].Message` and `truncated := resp.Truncated()`, BEFORE the model_turn emit, add a guard: if no tool calls AND not truncated AND blank content, set msg.Content = noContentYieldReport(resp.StopReason). Doing it before emit records the synthesized text on the event so reopen/replay reconstructs the identical non-empty turn with NO replay.go change.
- Helper noContentYieldReport(stopReason): switch on lowercased reason — "refusal" → declined note; ""/"end_turn"/"stop"/"stop_sequence" → "ended its turn without any content or tool call"; default → includes raw reason. Always non-empty.
- The `if len(msg.ToolCalls) == 0` block: truncated branch UNCHANGED (nudge/retry via truncatedStubContent + truncationNudge, bounded by maxTruncRetries — DO NOT touch, replay depends on it); else (yield) appends msg (non-empty content) and returns Report = msg.Content (never blank).
- Keep comments explaining the branching.

Replay: truncation-retry boundary untouched → replay.go stays correct; new path records synthesized text so replay appends verbatim — verify replay tests pass.

Tests (internal/engine): loop_test.go add non-truncated empty-yield test (StopReason "refusal") → non-empty Report mentioning the reason + non-empty stored assistant message; keep truncation tests green. config_test.go assert DefaultMaxTokens == 32000.

Build + go test ./... .

### Starting points
- internal/engine/loop.go: msg/truncated computed ~line 345-361, model_turn emit ~366, no-tool-call block ~391-421; truncatedStubContent/truncationNudge consts ~170; maxTruncRetries ~161
- internal/engine/replay.go: truncation boundary reconstruction lines 109-140 (do not break)
- internal/config/config.go: DefaultAnthropic ~363; cmd/ycc/main.go:540,604; internal/setup/setup.go:125
- gollama ResponseMessageGenerate.Truncated() returns true for stop_reason max_tokens/length

## Work log
- 2026-06-30 plan: Two parts.  PART A — raise default MaxTokens to a single shared constant. - Add `const DefaultMaxTokens = 32000` to internal/config/config.go (documented: per-turn output cap; larger so extended-thi
…[truncated]
- 2026-06-30 context hints: 4 recorded with plan
- 2026-06-30 context hints: internal/engine/loop.go: msg/truncated computed ~line 345-361, model_turn emit ~366, no-tool-call block ~391-421; truncatedStubContent/truncationNudge consts ~170; maxTruncRetries ~161; internal/engin
…[truncated]
- 2026-06-30 implementer report: Implemented Task 0093 in two parts.  PART A — raised default MaxTokens to a shared constant: - Added `const DefaultMaxTokens = 32000` to internal/config/config.go with documentation (per-turn output
…[truncated]
- 2026-06-30 review tier: single-opus — reviewers: Claude
- 2026-06-30 review (Claude): accept — The change satisfies all acceptance criteria. Default MaxTokens is raised to a shared constant (config.DefaultMaxTokens = 32000) and used consistently across the CLI flag, in-process daemon fallback, 
…[truncated]
- 2026-06-30 decision: accept — commit: engine/config: raise default MaxTokens to 32000 and robustly handle no-content yields (0093)
- 2026-06-30 usage: 26,059 tok (in 156, out 25,903, cache_r 2,829,245, cache_w 84,924) · cost n/a (unpriced)
  implementer: 16,339 tok (in 106, out 16,233, cache_r 1,762,714, cache_w 47,240) · cost n/a (unpriced)
  coordinator: 6,290 tok (in 28, out 6,262, cache_r 951,021, cache_w 19,935) · cost n/a (unpriced)
  reviewer:Claude: 3,430 tok (in 22, out 3,408, cache_r 115,510, cache_w 17,749) · cost n/a (unpriced)
