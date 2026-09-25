---
id: "0364"
title: Preserve typed provider errors and honor bounded Retry-After metadata
status: done
priority: 2
created: "2026-09-08"
updated: "2026-09-25"
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

## Outcome

Errors are now classified from typed metadata first (gollama.APIError, codex non-200 as APIError, codex in-stream StreamError codes with unchanged text), with string classification as the fallback. Retry timing uses Retry-After (seconds or date), then retry-after-ms, then for a 429 the latest Anthropic reset among exhausted buckets. The single loop retry ring waits max(backoff, provider wait) and stops early past MaxRetryAfter (5m) or MaxTotalWait (10m), both configurable. Waits stay cancellable. Retry and session_error events add code, retry timing and next_attempt_at/retry_at, with no header values exposed. TUI note and spec §7.2 are updated. Tests cover the acceptance cases.

Commit: engine: classify typed provider errors first and honor bounded Retry-After/reset guidance (0364)
