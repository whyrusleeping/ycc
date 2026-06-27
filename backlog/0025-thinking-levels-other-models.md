---
id: "0025"
title: Verify thinking levels (effort) across backends as models are added
status: todo
priority: 3
created: "2026-06-26"
updated: "2026-06-26"
depends_on:
    - "0005"
spec_refs:
    - Backends & model registry
    - Agent engine
---

## Description
gollama now supports Anthropic extended thinking + effort (done outside the ycc backlog, in
/home/why/code/gollama): `RequestOptions.Thinking` ("adaptive"), `Effort` ("low".."max"),
`ThinkingDisplay` map to the Anthropic `thinking` + `output_config.effort` fields; response
`thinking`/`redacted_thinking` blocks are parsed into `Message.Thinking` +
`Message.ThinkingBlocks` and replayed verbatim (signatures intact) so tool-using turns
round-trip. Verified offline + live against claude-opus-4-8 (gollama `thinking_test.go`,
`anthropic_live_test.go`).

**The translation is Anthropic-only.** Each backend expresses reasoning differently, so as we
wire more models into the registry we need to verify (and, where missing, implement) the
mapping per backend:
- **OpenAI / GPT** — `reasoning_effort` (low/medium/high) as a request field; gollama's OpenAI
  path does NOT send `Effort`/`Thinking` yet.
- **GLM** (OpenAI-compatible) — provider-specific thinking parameter; confirm shape.
- **Ollama** — `think` bool (the existing `RequestOptions.Think`); on/off only, no levels.

This task is the cross-backend verification pass; it pairs with the (separate) ycc-side work
to plumb per-role `effort`/`thinking` config (beside `max_tokens`) through config → session →
engine and to surface returned thinking in the event log/TUI.

## Acceptance criteria
- [ ] for each configured backend (anthropic ✓, openai/gpt, glm, ollama): confirm how it
      expresses thinking levels and that gollama translates `Effort`/`Thinking` into the right
      request shape (e.g. OpenAI `reasoning_effort`) — implement the missing translations
- [ ] live smoke test per backend: a reasoning prompt returns reasoning content and the
      request is accepted; a tool round-trip with thinking on does not error
- [ ] document the per-backend mapping (what "effort" means where; what's unsupported — e.g.
      Ollama is on/off only)
- [ ] decide + implement ycc behavior when a model doesn't support a requested level (silently
      ignore vs. error) so a per-role effort setting degrades gracefully across mixed backends

## Work log
