---
id: "0025"
title: Verify thinking levels (effort) across backends as models are added
status: blocked
priority: 3
created: "2026-06-26"
updated: "2026-07-03"
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
- 2026-07-05 re-blocked (autonomous coordinator): this session cannot complete the
  narrowed scope — the gollama working repo (/home/why/code/gollama) is still absent
  (only the read-only module cache exists) and no OPENAI_API_KEY is available, so the
  missing OpenAI translation cannot be implemented or smoke-tested. Verification done
  meanwhile against the pinned gollama (v0.0.0-20260628184513):
  - anthropic: `Thinking`/`Effort`/`ThinkingDisplay` → `thinking` + `output_config.effort`
    confirmed in anthropic.go (done, as recorded).
  - openai/openai-compatible/glm: `ChatCompletion`'s openaiRequest has NO
    `reasoning_effort` field; `Thinking`/`Effort` are `json:"-"` so they are silently
    dropped → degrade today is "silently ignore" (safe, but no reasoning control).
  - ollama: `Turn` routes Ollama through the OpenAI-compatible /v1 path, so the
    `Think` bool is also dropped (native /api/chat would carry it, but Turn never uses
    it). Local live check: Ollama /api/chat with `think:true` on gemma4:26b succeeds and
    returns a `thinking` field — the live smoke is ready to run once gollama plumbs it.
  Remaining (needs gollama repo + OpenAI key, user present): implement OpenAI
  `reasoning_effort` + Ollama `think` translation in gollama Turn path, live smokes,
  then the mapping doc + explicit degrade decision here. Note ycc already passes
  Effort/Thinking on every request regardless of backend, so gollama-side translation
  lights up without further ycc plumbing (spec §7.4, §13 already document
  ignored-harmlessly semantics).
- 2026-07-05 unblocked (pm grooming with user): scope narrowed to the backends the user
  has live access to right now — **OpenAI + Ollama** (Anthropic already done). GLM is
  deferred: keep its bullet in the description but treat it as out of scope for this
  pass; verify it later when an endpoint is available. Live smoke tests for OpenAI and
  Ollama are in scope since the user can attend/supply keys.
- 2026-07-02 blocked: parked for the overnight autonomous run — requires live smoke tests against OpenAI/GLM/Ollama backends (keys/endpoints not available unattended) and edits in the separate gollama repo; user wants to be present. Unblock when the user can supply/verify backend access.
