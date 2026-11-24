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

## Work log
