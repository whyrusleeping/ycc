---
id: "0307"
title: Overloaded api errors arent retried, a recent session hit this and halted
status: done
priority: 3
created: "2026-08-09"
updated: "2026-08-09"
depends_on: []
spec_refs: []
---

## Description

## Acceptance criteria

## Plan

Problem (confirmed from real session logs): provider "overloaded" errors delivered INSIDE an HTTP 200 stream carry no HTTP status code, so ClassifyAPIError (internal/engine/apierror.go) falls through to KindUnknown/not-retryable and the session halts. Two real occurrences:
- `anthropic stream error (overloaded_error): Overloaded` (oldgrowth s_953966aab09754ae, kind:"unknown", retryable:false)
- `codex: stream error: server_is_overloaded: Our servers are currently overloaded. Please try again later.` (oldgrowth s_ab86a526629343f7)

HTTP 503/529 already classify as KindOverloaded/retryable; only the in-stream (statusless) variants are broken.

Fix in internal/engine/apierror.go:
1. Add `providerOverloadedSignatures = []string{"overloaded_error", "server_is_overloaded", "overloaded"}` with a comment mirroring providerServerSignatures/providerRateLimitSignatures (Anthropic emits overloaded_error in-stream = 529 equivalent; codex emits server_is_overloaded). The generic "overloaded" catches other phrasings; safe because these lists are only consulted when NO status code parsed, so real 4xx bodies are unaffected.
2. In the no-status branch of ClassifyAPIError, check providerOverloadedSignatures (before or alongside the rate-limit/server signature loops) and return {Kind: KindOverloaded, Retryable: true}.
3. Also add "api_error" to providerServerSignatures — Anthropic's in-stream 500-equivalent error type, same class of bug waiting to happen. (Checked after rate-limit signatures so rate_limit_error still wins; note in the comment.)

Tests in internal/engine/apierror_test.go: add cases using the two verbatim real-world messages (expect KindOverloaded, Status 0, Retryable true), plus an anthropic in-stream api_error case (KindServer, retryable), and verify a 400 body mentioning "overloaded" still classifies as invalid_request (status precedence).

Verify: go build ./... && go test ./internal/engine/

### Starting points
- internal/engine/apierror.go — ClassifyAPIError, providerServerSignatures/providerRateLimitSignatures pattern to mirror
- internal/engine/apierror_test.go — table-driven test to extend
- real messages: 'anthropic stream error (overloaded_error): Overloaded' and 'codex: stream error: server_is_overloaded: Our servers are currently overloaded. Please try again later.'

## Work log
- 2026-08-09 plan: Problem (confirmed from real session logs): provider "overloaded" errors delivered INSIDE an HTTP 200 stream carry no HTTP status code, so ClassifyAPIError (internal/engine/apierror.go) falls through 
…[truncated]
- 2026-08-09 context hints: 3 recorded with plan
- 2026-08-09 context hints: internal/engine/apierror.go — ClassifyAPIError; mirror the providerServerSignatures / providerRateLimitSignatures pattern; internal/engine/apierror_test.go — table-driven TestClassifyAPIError to e
…[truncated]
- 2026-08-09 preload: 2 file(s), ~15 KiB seeded into implementer context
- 2026-08-09 implementer report: Implemented Task 0307 in `internal/engine/apierror.go` and its tests.  Changes: - Added statusless in-stream overload detection for `overloaded_error`, `server_is_overloaded`, and generic `overloaded`
…[truncated]
- 2026-08-09 review tier: single-opus — reviewers: sol
- 2026-08-09 review (sol): accept — The change satisfies Task 0307. Statusless Anthropic and Codex overload signatures now classify as retryable KindOverloaded errors, Anthropic api_error classifies as retryable KindServer, and parsed H
…[truncated]
