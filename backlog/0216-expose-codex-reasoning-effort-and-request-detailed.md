---
id: "0216"
title: Expose Codex reasoning effort and request detailed summaries
status: done
priority: 2
created: "2026-07-16"
updated: "2026-07-16"
depends_on: []
spec_refs:
    - Agent engine
    - Usage & cost accounting
---

## Description
Improve GPT/Codex reasoning observability. Request detailed reasoning summaries from the ChatGPT Codex Responses backend instead of provider-selected `auto` summaries, and preserve the backend's `output_tokens_details.reasoning_tokens` count through model-turn usage events so users can distinguish visible summary length from actual hidden reasoning effort.

Work must coexist with the current dirty tree and avoid disturbing unrelated in-progress changes.

## Acceptance criteria
- [ ] Codex requests with reasoning enabled ask for detailed summaries; reasoning-off requests still omit the reasoning block.
- [ ] Codex SSE parsing captures `output_tokens_details.reasoning_tokens`.
- [ ] The reasoning-token count is emitted on `model_turn` usage data and remains backward-compatible when absent.
- [ ] TUI reasoning rows show the count when available without implying that the displayed summary is the full chain of thought.
- [ ] Unit tests cover request shape, parsing, event propagation, and rendering.

## Work log
