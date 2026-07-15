---
id: "0193"
title: Anthropic subscription (OAuth) auth support
status: done
priority: 3
created: "2026-07-15"
updated: "2026-07-15"
depends_on: []
spec_refs:
    - 13. Configuration
    - 20.4 Cost accounting
---

## Description
Support authenticating the anthropic backend with a Claude Pro/Max subscription (OAuth) instead of an API key. Anthropic recently relaxed third-party subscription usage, so no Claude-Code system-prompt spoofing is needed.

Scope:
- `internal/anthropicauth`: PKCE authorize-URL generation, code exchange, token refresh against `https://console.anthropic.com/v1/oauth/token`; credentials (access, refresh, expiry) persisted as JSON in the machine-local secrets store under `ANTHROPIC_OAUTH`.
- `ycc login anthropic` CLI command: print/open the authorize URL, accept the pasted `code#state`, exchange, store. Logout = `ycc token remove ANTHROPIC_OAUTH`.
- Config: `auth = "oauth"` on `[models.X]` (anthropic backend only; validated). `Registry.Build` then resolves a fresh access token (auto-refresh, persisted) and sends `Authorization: Bearer` + `anthropic-beta: oauth-2025-04-20` instead of `x-api-key`.
- Long-lived `claude setup-token` tokens (`sk-ant-oat…`) resolved via the normal key_env path are auto-detected and get the same bearer+beta treatment.
- Model discovery (`discoverAnthropic`) sends bearer+beta for `sk-ant-oat…` keys.
- Cost accounting: subscription usage is prepaid — `auth = "oauth"` models default to unpriced (spec §20.4 "never invent numbers"); explicit config price_* still wins.
- Doctor check for oauth-auth models (creds present / refresh token / expiry).
- Spec §13 updated.

Acceptance:
- [ ] `ycc login anthropic` completes the PKCE flow and persists credentials
- [ ] a model with `auth = "oauth"` runs turns using the subscription (auto-refreshing expired access tokens)
- [ ] expired/absent refresh token yields a clear "run `ycc login anthropic`" error
- [ ] unit tests: PKCE/exchange/refresh (httptest), config validation, header selection

## Acceptance criteria

## Work log
