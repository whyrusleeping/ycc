---
id: "0431"
title: 'Credential transport hygiene: redirect policy, error-body redaction, webhook URL logging'
status: todo
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

## Work log
