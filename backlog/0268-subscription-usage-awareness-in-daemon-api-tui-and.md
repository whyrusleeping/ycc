---
id: "0268"
title: Subscription usage awareness in daemon API, TUI, and iOS app
status: done
priority: 3
created: "2026-07-16"
updated: "2026-08-06"
depends_on: []
spec_refs:
    - Credential mechanisms
    - Usage and cost accounting
---

## Description
Expose provider-reported subscription usage windows for OAuth-backed Anthropic and OpenAI models through the daemon, and present a concise freshness-aware summary in the terminal UI and iOS app. This task is informational only: limit failure/failover behavior remains unchanged.

Acceptance criteria:
- Define a provider-neutral subscription-usage model covering utilization, reset time/window, scope/label, availability, and freshness.
- Query available provider usage data without blocking model turns; cache and throttle refreshes, and degrade safely when unavailable.
- Add a daemon RPC returning subscription usage for configured OAuth models/accounts.
- Display concise utilization/reset information in the TUI with a detailed view and explicit stale/unavailable states.
- Display the same account/model subscription usage in the iOS app.
- Never expose OAuth access or refresh tokens through the RPC, logs, or UI.
- Add Go and Swift tests, regenerate protobuf bindings, and update durable design docs.
- Do not change retry, failover, or limit-exhaustion behavior in this task.

## Acceptance criteria

## Work log
- 2026-08-06 renumbered 0211 → 0268 (duplicate id detected, 0211 kept by another task)
- 2026-08-07: Closed done in a backlog audit — implemented and committed; on-device use is the verification the Linux box could not provide.
