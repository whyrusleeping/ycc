---
id: "0201"
title: 'Codex: honor max_output_tokens and truncation semantics'
status: todo
priority: 2
created: "2026-07-15"
updated: "2026-07-15"
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

## Work log
