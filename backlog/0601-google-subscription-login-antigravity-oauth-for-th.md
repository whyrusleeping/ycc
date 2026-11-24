---
id: "0601"
title: Google subscription login (Antigravity OAuth) for the gemini backend
status: done
priority: 3
created: "2026-11-24"
updated: "2026-11-24"
depends_on:
    - "0595"
spec_refs: []
---

## Description

Status: todo. Needs 0595 (the native Gemini backend) landed first.

## Acceptance criteria

- After `ycc login google`, a `gemini` model with `auth = "oauth"` runs a tool-using session on Neon.
- The token refreshes without user action.
- A suspension or ineligibility response surfaces as a clear auth error and is not retried.
- No credential appears in events, diagnostics or RPC responses.

## Outcome

`ycc login google` and the daemon RPCs Begin/Complete/CancelGoogleLogin log in with agy's own Antigravity CLI OAuth flow (the hosted antigravity.google/oauth-callback paste-code page, PKCE). Credentials live in GOOGLE_OAUTH in the secrets store and refresh by compare-and-swap. A gemini model with auth = "oauth" routes through internal/antigravity, which runs gollama's Gemini client unchanged behind a RoundTripper. The RoundTripper handles the envelope, project discovery, wire model ids from the account catalog, the parameters schema allowlist and SSE unwrapping. Ineligibility, a missing or rejected login, and TOS_VIOLATION are non-retryable auth errors; a suspension blocks the grant in-process and persistently. The web settings, login dialog, CLI, spec and README all warn that the path is unofficial. Verified live on 2026-11-24 with the owner's account: a forced refresh, a 42-model catalog with gemini-4-neon-{low..xhigh}, and a two-step Neon tool call through Registry.Build (project aicode-consumers persisted).

Commit: Google subscription login and Antigravity transport for the gemini backend (0601)
