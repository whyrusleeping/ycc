---
id: "0201"
title: 'Codex: honor max_output_tokens and truncation semantics'
status: done
priority: 2
created: "2026-07-15"
updated: "2026-08-07"
depends_on: []
spec_refs:
    - Agent engine
    - Backends & model registry
---

## Description
The engine maps the configured per-turn cap into `gollama.RequestOptions.Options.MaxTokens`, but `internal/codex` does not include Responses API `max_output_tokens` in its request. ChatGPT OAuth sessions therefore ignore ycc's configured output cap and differ from other backends.

## Acceptance criteria
- [ ] Codex requests set `max_output_tokens` when `opts.Options.MaxTokens > 0` and omit it when no cap is configured.
- [ ] The wire value matches the engine/configured per-turn cap exactly.
- [ ] `response.incomplete` with `incomplete_details.reason == "max_output_tokens"` continues to map to the engine's standard truncated response behavior, including a turn that produces no visible text before truncation.
- [ ] Tests inspect the serialized request for configured and unset caps and cover visible-text and reasoning-only truncation cases.
- [ ] Usage accounting remains correct.
- [ ] `go test ./...` passes.

## Plan

Goal: Codex (ChatGPT OAuth Responses API) requests must honor the engine's configured per-turn output cap.

1. In internal/codex/codex.go:
   - Add `MaxOutputTokens int `json:"max_output_tokens,omitempty"`` to the `request` struct (placed near Model/Instructions fields).
   - In `buildRequest`, set `req.MaxOutputTokens = opts.Options.MaxTokens` when `opts.Options != nil && opts.Options.MaxTokens > 0`. When no cap is configured (nil Options or MaxTokens <= 0), the field must be omitted from the serialized JSON (omitempty handles this).
2. Tests in internal/codex/codex_test.go:
   - New test that serializes buildRequest output (json.Marshal) and asserts `"max_output_tokens":N` appears with the exact configured value when Options.MaxTokens is set, and that the key is absent when Options is nil and when MaxTokens is 0.
   - Truncation: TestTurnIncompleteMaxTokens already covers visible-text truncation (StopReason mapped to "length", resp.Truncated() true). Add a reasoning-only truncation case: stream only reasoning summary deltas (no output_text), then `response.incomplete` with reason max_output_tokens — assert no error, resp.Truncated() true, and empty visible content (parseStream sets completed=true on response.incomplete, so it must not fall into the "stream ended without a completed response" error path). Also assert usage fields carried through (usage accounting unchanged).
3. Run `go build ./... && go test ./internal/codex/...` and then `go test ./...` (note memory.md: some pre-existing flaky tests in internal/session, internal/setup, internal/tools — compare against HEAD before blaming).

Notes: the engine already maps the configured cap into gollama.RequestOptions.Options.MaxTokens (internal/engine/loop.go ~line 762), so no engine change is needed. gollama's OpenAI adapter uses the same `opts.Options.MaxTokens > 0` guard — mirror it.

### Starting points
- internal/codex/codex.go: `request` struct (~line 133) and `buildRequest` (~line 148); truncation mapping at ~line 585 already maps incomplete/max_output_tokens -> StopReason "length"
- internal/codex/codex_test.go: TestTurnIncompleteMaxTokens (~line 434) and the `sse` helper pattern
- internal/engine/loop.go:762 — opts.Options = &gollama.Options{MaxTokens: l.MaxTok}
- gollama openai.go:141 uses `if opts.Options.MaxTokens > 0` guard (note: check opts.Options != nil first; codex path may receive nil Options)

## Work log
- 2026-08-07 plan: Goal: Codex (ChatGPT OAuth Responses API) requests must honor the engine's configured per-turn output cap.  1. In internal/codex/codex.go:    - Add `MaxOutputTokens int `json:"max_output_tokens,omitem
…[truncated]
- 2026-08-07 context hints: 4 recorded with plan
- 2026-08-07 context hints: internal/codex/codex.go: `request` struct ~line 133, `buildRequest` ~line 148, truncation mapping ~line 585 (response.incomplete -> StopReason "length"); internal/codex/codex_test.go: TestTurnIncomple
…[truncated]
- 2026-08-07 preload: 1 file(s), ~4 KiB seeded into implementer context
- 2026-08-07 implementer report: Implemented task 0201. Added `request.MaxOutputTokens` with `json:"max_output_tokens,omitempty"` and populated it only when `opts.Options != nil && opts.Options.MaxTokens > 0`, preserving the configur
…[truncated]
- 2026-08-07 review tier: simple (coordinator self-review)
- 2026-08-07 decision: accept — commit: Codex: send max_output_tokens from the configured per-turn cap (task 0201)
- 2026-08-07 usage: 327,845 tok (in 140,319, out 8,326, cache_r 448,396, cache_w 16,942) · cost n/a (unpriced)
  implementer: 322,963 tok (in 140,297, out 3,466, cache_r 179,200, cache_w 0) · cost n/a (unpriced)
  coordinator: 4,882 tok (in 22, out 4,860, cache_r 269,196, cache_w 16,942) · cost n/a (unpriced)
