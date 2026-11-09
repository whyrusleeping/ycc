---
id: "0507"
title: ChatGPT subscription login from the browser (daemon RPCs + web)
status: in_review
priority: 2
created: "2026-11-09"
updated: "2026-11-09"
depends_on: []
spec_refs:
    - spec.md#13. Models, credentials, and review tiers
    - spec.md#19. Onboarding
---

## Description
ChatGPT (Plus/Pro) subscription login is CLI-only (`ycc login openai`). Newcomers setting up from the web app need it in the browser, like the Anthropic connect.

Acceptance:
- Daemon-owned OpenAI login RPCs (begin / complete / cancel) mirroring the Anthropic ones: one pending attempt per daemon, ten-minute expiry, consumed on submit, state verified, provider errors sanitized, URLs/codes never logged or put in events. Completion takes the URL the browser was redirected to (`http://localhost:1455/auth/callback?code=…&state=…`), since the browser usually can't reach the daemon's localhost.
- On a daemon still on the untouched built-in fallback with no working key, completing it sets up an `auth = "oauth"` ChatGPT model for all roles and persists it (replacing the never-chosen fallback model); any other config is untouched.
- Web: Settings → Accounts and the Get started checklist offer "Connect ChatGPT…" with the paste-the-redirect-URL flow.
- Go, Swift, and TypeScript bindings regenerated; spec §13/§19.1 and docs/remote-api.md updated.

## Acceptance criteria

## Work log

- 2026-11-09: Implemented. Daemon: BeginOpenAILogin/CompleteOpenAILogin/CancelOpenAILogin (internal/server/openai_login.go; fixed localhost:1455 redirect, never bound; pasted callback must carry the attempt's state), openaiauth refresh/login compare-and-swap so an old refresh can't overwrite a new login, ChatGPT adoption of the untouched fallback (replaces `claude` with `chatgpt`, all roles). Go/Swift/TS bindings regenerated; spec §13/§19.1 and docs/remote-api.md updated. Web: the login dialog now serves both providers (Settings → Models, Get started, palette) and refreshes models + health after a login. Daemon side implemented by a delegated agent (sol); reviewed by opus: no blocking defects, the stale-Settings-after-login issue fixed. Verified in the browser against a scratch daemon up to the provider redirect (no real ChatGPT login). Uncommitted.
