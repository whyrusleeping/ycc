---
id: "0050"
title: Auto-retry transient LLM API call failures with backoff
status: in_progress
priority: 3
created: "2026-06-27"
updated: "2026-06-28"
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
- 2026-06-28 plan: Add an LLM-call retry layer with exponential backoff + jitter, wrapping the single backend chokepoint.  1. New file internal/engine/retry.go:    - RetryPolicy{MaxAttempts, BaseDelay, MaxDelay} + Defau
…[truncated]
- 2026-06-28 implementer report: Implemented auto-retry of transient LLM API call failures with exponential backoff + jitter.  Changes: 1. New file `internal/engine/retry.go`:    - `RetryPolicy{MaxAttempts, BaseDelay, MaxDelay}` and 
…[truncated]
- 2026-06-28 review tier: single-opus — reviewers: Claude
- 2026-06-28 review (Claude): accept — Task 0050 is implemented correctly and cleanly. A new `internal/engine/retry.go` adds a `RetryPolicy`/`DefaultRetryPolicy` (3 attempts, 500ms base, 30s cap) and a `WithRetry` decorator over the `Turne
…[truncated]
