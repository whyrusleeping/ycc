---
id: "0601"
title: Google subscription login (Antigravity OAuth) for the gemini backend
status: todo
priority: 3
created: "2026-11-24"
updated: "2026-11-24"
depends_on:
    - "0595"
spec_refs: []
---

## Description
Status: todo. Needs 0595 (the native Gemini backend) landed first.

## Intent
Use a Google AI Pro/Ultra subscription login (instead of a paid Gemini API key) to run `gemini-4-neon` in ycc. User decision, 2026-11-24 (session s_49a8e36e63b22132): build it anyway at the user's own risk, after being told the facts below.

## Facts (research, 2026-11-24)
- Google shut off Gemini CLI/Code Assist inference for consumer Free/Pro/Ultra accounts on 2026-06-18 (https://developers.google.com/gemini-code-assist/docs/deprecations/code-assist-individuals).
- Neon on Pro/Ultra subscriptions is served only through Antigravity (2026-11-18 rollout).
- The Antigravity terms (§6) and Gemini CLI terms forbid third-party reuse of their OAuth credentials. Google has suspended accounts for it (Feb, Mar and Nov 2026). Cline, Roo, Crush, LiteLLM and pi removed or declined this kind of integration.
- The Antigravity wire protocol is unofficial and reverse-engineered (for example by the opencode-antigravity-auth plugin), so it can change without notice.

## Scope
- `ycc login google`: a daemon-owned login, available both from the CLI and remotely through the same begin/complete/cancel RPC shape the Claude and ChatGPT logins use. Credentials are stored machine-locally, refreshed with a compare-and-swap, and never returned in RPC responses.
- `backend = "gemini"` with `auth = "oauth"` routes through an Antigravity transport: gollama's Gemini content mapping wrapped in the Antigravity request/response envelope, with the project/onboarding lookup.
- The settings UI and docs state that this path is unofficial and can get the account suspended.

## Acceptance criteria
- After `ycc login google`, a `gemini` model with `auth = "oauth"` runs a tool-using session on Neon.
- The token refreshes without user action.
- A suspension or ineligibility response surfaces as a clear auth error and is not retried.
- No credential appears in events, diagnostics or RPC responses.

## Protocol notes
Research from 2026-11-24 (session s_49a8e36e63b22132). Every point is observed community behavior, not a Google contract; sources disagree in places, as noted.
- **OAuth.** The installed-app client id `1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com` (secret published in opencode-antigravity-auth `src/constants.ts`, also used by `agy`). Authorize at `https://accounts.google.com/o/oauth2/v2/auth` with `response_type=code`, `access_type=offline`, `prompt=consent`, `state`, and PKCE S256. Scopes under `https://www.googleapis.com/auth/`: `cloud-platform userinfo.email userinfo.profile cclog experimentsandconfigs` (some implementations add `aicode`). The redirect is `http://localhost:51121/oauth-callback`; for a headless daemon the user pastes the failed callback URL, as with the ChatGPT flow, and `state` must match. Exchange and refresh go to `https://oauth2.googleapis.com/token` (form POST including `code_verifier`). Keep the old refresh token if a refresh omits it; `invalid_grant` means re-login. The account email comes from `GET https://www.googleapis.com/oauth2/v1/userinfo?alt=json`.
- **Host and project.** Host `https://daily-cloudcode-pa.googleapis.com`. `POST /v1internal:loadCodeAssist` with `{"metadata":{"ideType":"ANTIGRAVITY"}}` returns `cloudaicompanionProject` (a string or `{id}`), `currentTier`, `paidTier` (`g1-pro-tier`/`g1-ultra-tier`), and `ineligibleTiers[]`, whose `reasonCode`/`reasonMessage` surface as the ineligibility error. If the account isn't onboarded, `POST /v1internal:onboardUser` with `{"tierId":"free-tier","metadata":{"ideType":"ANTIGRAVITY"}}`, then poll `GET /v1internal/{operation.name}`. Sources disagree on the field casing for this call. Use the project returned; never a hardcoded fallback.
- **Inference.** `POST /v1internal:streamGenerateContent?alt=sse` with a bearer token. The body is `{project, model, requestId, userAgent:"antigravity", request:{contents, systemInstruction (a Content object), tools, toolConfig, generationConfig, sessionId}}`. The model id comes from `POST /v1internal:fetchAvailableModels {"project":…}`; Neon was observed on 2026-11-19 as `gemini-4-neon-low|medium|high|xhigh`, paired with a matching `thinkingLevel`. Tool schemas use sanitized `functionDeclarations[].parameters`, not `parametersJsonSchema`. Each SSE event and unary body wraps the normal response as `{response:{…}, traceId}`, so unwrap per event and check for a top-level `error`.
- **Errors.**
  - A 429 is `RESOURCE_EXHAUSTED`, with `ErrorInfo.reason` (`QUOTA_EXHAUSTED`/`RATE_LIMIT_EXCEEDED`) and `RetryInfo.retryDelay`.
  - A suspension is a 403 whose `ErrorInfo.reason` is `TOS_VIOLATION` (possibly with `metadata.appeal_url`). Stop all use of that grant and don't retry.
  - `retrieveUserQuotaSummary` reports remaining quota.
- **Fragility.** Implementations keep chasing changing client identifiers and headers. Suspensions were reported even for clients mimicking the official transport.
- **Sources.** https://github.com/NoeFabris/opencode-antigravity-auth (2026-10-27 source; `docs/ANTIGRAVITY_API_SPEC.md`), can1357/oh-my-pi PRs 19787 and 19789 (Neon trace and live test, 2026-11-19), router-for-me/CLIProxyAPI v8.1.2, https://antigravity.google/terms/ §6.
- **Reuse.** gollama c6eaaa5's Gemini content mapping, SSE decoder, signature replay and APIError handling, behind an envelope and auth layer.

## Work log
