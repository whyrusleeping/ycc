---
id: "0172"
title: Run the OpenAI reasoning_effort live smoke (needs OPENAI_API_KEY)
status: blocked
priority: 3
created: "2026-07-06"
updated: "2026-07-06"
depends_on:
    - "0025"
spec_refs:
    - Backends & model registry
    - Agent engine
---

## Description
Split out of task 0025 (cross-backend thinking/effort verification): everything landed except *executing* the OpenAI live smoke, because no OPENAI_API_KEY is available in the environment (checked env + ~/.config/ycc/secrets.json). The test itself already exists and is key-guarded.

## What's in place (gollama c92bd8b)
- OpenAI-compatible path translates `RequestOptions.Effort` → `reasoning_effort` (low/medium/high/xhigh pass through; `max` clamps to `xhigh`).
- `openai_live_test.go` in the gollama repo: guarded by `OPENAI_API_KEY`, `t.Skip`s without it; runs a reasoning turn against api.openai.com with `Effort="low"`.

## Steps (user supplies the key)
1. In /home/why/code/gollama: `OPENAI_API_KEY=... go test -run TestOpenAI -v ./...` (or the specific live test name).
2. Verify: the request is accepted (no 400 on `reasoning_effort`), an answer comes back, and — model permitting — reasoning happens (token usage/behavior consistent with the effort level).
3. Also smoke a tool round-trip with reasoning on against OpenAI (mirrors the Ollama live smoke) — extend the live test if it doesn't already cover it.
4. If the accepted `reasoning_effort` values differ from the documented set (none|minimal|low|medium|high|xhigh) or `xhigh` is rejected for the chosen model, adjust `mapOpenAIEffort` / the model choice and update the mapping docs (gollama types.go comments + ycc spec §7.4 table).

## Acceptance criteria
- [ ] gollama's OpenAI live reasoning smoke run with a real key and passing
- [ ] tool round-trip with reasoning on succeeds against OpenAI
- [ ] any mapping corrections discovered are landed in gollama + ycc spec §7.4

## Work log
