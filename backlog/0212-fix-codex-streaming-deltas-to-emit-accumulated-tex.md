---
id: "0212"
title: Fix Codex streaming deltas to emit accumulated text snapshots
status: done
priority: 2
created: "2026-07-15"
updated: "2026-07-15"
depends_on: []
spec_refs:
    - spec.md#Transient (broadcast-only) events
---

## Description
Codex `TurnStream` currently invokes the engine callback with each raw output-text fragment, despite the `StreamTurner`/`turn_delta` contract requiring full accumulated snapshots. As a result, live clients replace the chat tail with one token/word at a time; the completed durable `model_turn` is correct.

## Acceptance criteria
- Codex `TurnStream` callbacks contain the full accumulated assistant text after each fragment.
- A regression test covers multiple fragments and verifies monotonic accumulated snapshots.
- Existing Codex and engine streaming tests pass.

## Work log
