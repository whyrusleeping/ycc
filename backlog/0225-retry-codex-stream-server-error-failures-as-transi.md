---
id: "0225"
title: Retry Codex stream server_error failures as transient
status: done
priority: 2
created: "2026-07-28"
updated: "2026-07-28"
depends_on: []
spec_refs:
    - "7.2"
---

## Description
Codex may emit an in-stream structured error with `type`/`code` `server_error` and a message explicitly saying the request can be retried. The Codex adapter currently stringifies that event without an HTTP status, so engine classification falls through to unknown/non-retryable and the user must send “continue.” Classify this specific provider server error as retryable `server`, preserving bounded loop retry/backoff. Add regression tests for classification and retry recovery; keep permanent Codex errors non-retryable.

## Acceptance criteria

## Work log
