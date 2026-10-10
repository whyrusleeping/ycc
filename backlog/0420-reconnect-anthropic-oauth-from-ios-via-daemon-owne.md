---
id: "0420"
title: Reconnect Anthropic OAuth from iOS via daemon-owned browser-and-paste login
status: done
priority: 2
created: "2026-10-03"
updated: "2026-10-10"
depends_on: []
spec_refs: []
---

## Description
Implement the agreed phone-only Anthropic reconnect flow: daemon creates a short-lived OAuth attempt, iOS opens the provider browser page, user pastes code#state into a dedicated login field, daemon exchanges and securely saves credentials. Access/refresh tokens stay daemon-side; no automatic work-loop restart.

## Acceptance criteria
- Authenticated begin/complete/cancel RPCs with bounded, expiring, single-use attempts and strict state validation; no credential material in errors, logs, or session events.
- iOS entry points in global provider settings and work-loop stopped screen, actionable success/failure states, browser opening and manual code paste.
- Existing auth=oauth models pick up replacement credentials without daemon restart; model configuration and work-loop state are not changed by login.
- Both Go and Swift bindings regenerated; security/lifecycle tests cover expiry, replay, invalid state, concurrency, and cancellation.

## Work log
- Added authenticated Begin/Complete/CancelAnthropicLogin RPCs: one memory-only ten-minute attempt, strict code#state validation, consume-before-exchange, safe errors, daemon-only credential storage. Both Go/Swift bindings regenerated reproducibly.
- Added a dedicated secure-code iOS sheet from provider settings and Anthropic-related loop failures. Handles cancellation and late responses; never edits model roles or restarts work.
- Refresh persistence now uses atomic cross-process compare-and-swap, so an older in-flight refresh cannot overwrite the new login or delay its save. Regression coverage includes a subprocess and completion while an old refresh is blocked.
- Full race suites pass for internal/anthropicauth, internal/secrets, and internal/server. Nine Swift model tests pass in a Docker Swift 6.2 scratch package with real generated protobuf types. Security and iOS lifecycle reviews found no remaining blockers after fixes.
- Full Go suite hit the local TestRepositoryDocsConfig mismatch and an intermittent TestSubscribeAfterRestartAndResubscribe failure (20 isolated repetitions passed; the same live-seq-0 failure reproduced in 30 repetitions on a clean HEAD archive); all login tests pass. git diff --check passes.
- Awaiting Xcode/on-device browser/paste verification. Source changes and generated bindings are not committed or deployed; daemon/app both require updating to use the new flow.
