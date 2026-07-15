---
id: "0195"
title: OpenAI ChatGPT subscription (OAuth/Codex) auth support
status: in_review
priority: 3
created: "2026-07-15"
updated: "2026-07-15"
depends_on: []
spec_refs:
    - 13. Backends & model registry
---

## Description
Support authenticating OpenAI models with a ChatGPT subscription (Plus/Pro) instead of an API key, mirroring the Anthropic subscription support (0193/0194).

Unlike Anthropic (same /v1/messages endpoint, different header), ChatGPT subscription inference goes to the **Codex backend** — `https://chatgpt.com/backend-api/codex/responses` — which speaks the **Responses API** (streaming-only, store:false, mandatory instructions), so a dedicated transport is needed (gollama only speaks /chat/completions).

Scope:
- `internal/openaiauth`: OAuth 2.0 + PKCE against auth.openai.com (Codex CLI public client id `app_EMoamEEZ73f0CkXaXp7hrann`), loopback callback on `http://localhost:1455/auth/callback`, form-encoded code exchange, JSON refresh; `chatgpt_account_id` parsed from the id_token JWT; credentials persisted in the secrets store under `OPENAI_OAUTH`; `AccessToken()` auto-refresh.
- `internal/codex`: an engine.Turner + StreamTurner that translates gollama.RequestOptions → Responses API (input items incl. function_call / function_call_output, function tools, reasoning effort) and parses the SSE stream (output_text deltas, function_call items, reasoning summaries, usage from response.completed). Required headers: Authorization bearer, chatgpt-account-id, originator, `OpenAI-Beta: responses=experimental`. Errors formatted "non-200 status code NNN: body" so engine.ClassifyAPIError works unchanged.
- Config: `auth = "oauth"` now valid for the openai backend too; `Registry.Build` routes such models through the codex transport (default base URL swapped to the codex backend when base_url is empty/api.openai.com).
- `ycc login openai`: browser flow with local callback server.
- TUI backend form + setup wizard: auth picker allows oauth for openai; wizard uses a wait-for-browser login mode (vs anthropic's paste-code mode).
- Doctor check for openai-oauth models.
- Spec §13/§19.1 updated; unit tests (auth flow against httptest, request build, SSE parsing, config wiring).

Acceptance:
- [ ] `ycc login openai` completes browser OAuth and persists credentials
- [ ] an openai model with `auth = "oauth"` runs turns (incl. tool calls) via the codex backend
- [ ] auto-refresh of expired access tokens; clear "run ycc login openai" error otherwise
- [ ] unit tests for auth, transport request/SSE mapping, config validation

## Acceptance criteria

## Work log
