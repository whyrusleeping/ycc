---
id: "0364"
title: Preserve typed provider errors and honor bounded Retry-After metadata
status: blocked
priority: 2
created: "2026-09-08"
updated: "2026-09-09"
depends_on: []
spec_refs:
    - §7.2 Loop, repair, and failure handling
    - §13 Models, credentials, and review tiers
---

## Description
Provider HTTP errors currently lose structured status/code/headers at the gollama boundary, forcing string classification and guessed backoff. Preserve transport metadata and use provider retry guidance. This complements, but does not automatically accept, the broader subscription-reset scheduling idea in 0306.

## Acceptance criteria
- Provider errors expose status, provider code, safe diagnostic text, and relevant retry metadata; normalized in-stream HTTP-200 errors remain supported.
- Classify structured fields first with conservative compatibility fallbacks for legacy transports.
- Honor Retry-After seconds/date forms and supported reset metadata with explicit bounds, cancellation, and total retry budgets.
- Retry events expose a truthful next-attempt time/reason for clients without leaking credentials or raw sensitive headers.
- Tests cover missing/malformed/past/excessive retry values, 429/5xx/auth/context failures, in-stream errors, and cancellation during wait.
- Coordinate dependency changes and avoid adding a second hidden retry loop.

## Work log
- 2026-09-09: Blocked on dependency access/publication, as for 0363. Inspected gollama http.go:doWithRetry: it closes the HTTP response and returns only formatted status/body text, discarding retry headers. The private HTTP client has no supported injection boundary. Need authorization to modify/publish gollama (sibling is outside writable roots), or a published revision preserving typed status/code/retry metadata; cannot implement the full acceptance criteria solely in ycc without an unsupported workaround.
