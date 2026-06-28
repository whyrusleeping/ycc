---
id: "0050"
title: Auto-retry transient LLM API call failures with backoff
status: todo
priority: 3
created: "2026-06-27"
updated: "2026-06-27"
depends_on: []
spec_refs: []
---

## Description
## Description

LLM API calls can fail due to transient issues (network errors, timeouts, rate limiting). These failures should be automatically retried a couple of times rather than failing the whole operation immediately.

Retries should be **limited to transient/retryable failures** only:
- Network errors / connection failures
- Timeouts
- HTTP 429 (rate limited)
- HTTP 5xx (server errors)

**Non-retryable errors must fail immediately** (no retry):
- Auth errors (401/403)
- Bad request / validation errors (most 4xx)

Use exponential backoff between retries (ideally with jitter) and cap the number of attempts.

## Acceptance Criteria

- [ ] Transient failures (network/timeout/429/5xx) are automatically retried at least a couple of times.
- [ ] Retries use exponential backoff (with jitter if practical).
- [ ] A configurable/sensible maximum retry count is enforced; the original error surfaces after retries are exhausted.
- [ ] Non-retryable errors (auth/4xx bad request) fail immediately without retrying.
- [ ] Retry attempts are observable (e.g. logged) for debugging.

## Acceptance criteria

## Work log
