---
id: "0431"
title: 'Credential transport hygiene: redirect policy, error-body redaction, webhook URL logging'
status: done
priority: 3
created: "2026-10-06"
updated: "2026-10-06"
depends_on: []
spec_refs: []
---

## Description

From security audit 0427 (F7, F8, F10).
- x-api-key survives cross-origin redirects (Go only strips Authorization): internal/config/discover.go, internal/tools/web.go, internal/llmhttp/policy.go. Add a CheckRedirect that rejects cross-origin redirects and https→http downgrades on credential-bearing clients.
- Raw provider/OAuth error bodies (codex.go:469-471, anthropicauth.go:203-205, openaiauth.go:363-365, gollama APIError) flow into session_error events, clients, and notifications. Redact the request's own credentials before emitting.
- internal/notify logs the full webhook URL on failure. Log the origin only.
- Web client: add a "forget token" action and allow-list raster mediaTypes before creating blob URLs.

## Acceptance criteria

Each item fixed with a unit test; web dist rebuilt.

## Outcome

F7: llmhttp.CheckRedirect (same scheme/host/port, no https→http, 10-hop cap) installed on inference, codex, discovery, Exa, OAuth token endpoints, notifier, subusage and daemon RPC clients. F8: llmhttp.Redact/RedactError strip the request's own credentials (long ones as substrings, short ones as whole tokens) from codex, OAuth token-endpoint and registry-built gollama errors while preserving APIError metadata/classification. F10: notifier logs webhook origin only; web client gains an explicit Forget token action and only renders JPEG/PNG/GIF/WebP as image blobs. Unit tests for each; embedded web dist rebuilt from a clean checkout (user WIP excluded).

Commit: security: same-origin redirect policy on credential-bearing HTTP clients; redact request credentials from provider/OAuth errors; origin-only webhook logging; web forget-token action and raster-only blob previews (0431)
