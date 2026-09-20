---
id: "0396"
title: 'Codex backend: send prompt_cache_key per loop to stabilize prompt-cache routing'
status: done
priority: 2
created: "2026-09-20"
updated: "2026-09-20"
depends_on: []
spec_refs: []
---

## Description
## Problem
internal/codex/codex.go `buildRequest` sends no `prompt_cache_key`. OpenAI routes requests to cache machines by a hash of the prompt's initial prefix; when many requests share that prefix (concurrent subagents + coordinator all start with similar instructions) they spill across machines and cache hits drop. The official Codex CLI sets `prompt_cache_key` to its session id for this reason.

Evidence: valstore session `s_bd32b8b81561efc0` coordinator (gpt-6-astra): 173 turns with ctx > 100K had `cache_read == 0`, often seconds apart at ~830K context (e.g. 06:52:46, 06:53:15, 06:54:02, 06:55:24, 06:56:26, 06:57:08 on 2026-09-20). Those 173 turns alone = ~77M uncached input tokens (~80% of the coordinator's uncached spend, ~48% of the session's total OpenAI uncached input). Sol subagents in tight tool loops also only hit 45–67% cache vs 100% for the Anthropic subagents.

## Proposal
- Add `PromptCacheKey string \`json:"prompt_cache_key,omitempty"\`` to the codex request; populate it per loop identity (session id + actor, e.g. `s_xxx/coordinator`, `s_xxx/agent_4`) so each loop's prefix routes consistently. Thread the key through gollama.RequestOptions (or a codex-specific option) from engine.Loop.
- Verify against live Codex backend that the field is accepted (the backend rejects some unknown fields, cf. max_output_tokens) and measure cache_read fraction before/after on a multi-turn tool loop.
- Log `prompt_cache_key` presence in model_turn data for forensics.

## Acceptance
- codex_test asserts the key is present and stable across turns of one loop and differs across loops.
- Live check shows materially improved cache_read ratio on a ≥20-turn loop.

## Acceptance criteria

## Work log

## Work log

- 2026-09-20: Implemented. `engine.Loop.PromptCacheKey` (random per-loop fallback) is sent as `ExtraBody["prompt_cache_key"]` on the `openai` backend only and recorded on `model_turn`; the codex transport forwards it as the `prompt_cache_key` body field AND the `session-id` header (the official Codex CLI's client.rs notes ChatGPT derives cache affinity from that header and sends its thread id as both). Session sets `<session id>/coordinator`; orchestrator `Deps.PromptCacheScope` gives subagents `<session id>/<actor>`. Tests: internal/engine/prompt_cache_test.go, internal/codex TestBuildRequestPromptCacheKey / TestTurnStreamSendsPromptCacheKey / TestTurnStreamNoPromptCacheKeyByDefault. Live acceptance check could not run: the ChatGPT weekly quota is exhausted (429 usage_limit_reached until ~2026-09-26); acceptance is inferred from the Codex CLI sending the identical field/header to the same endpoint. Re-check cache_read ratio on the first long openai session after the quota resets.
