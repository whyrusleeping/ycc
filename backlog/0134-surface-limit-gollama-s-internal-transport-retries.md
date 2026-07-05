---
id: "0134"
title: Surface/limit gollama's internal transport retries (uncancellable, invisible)
status: todo
priority: 4
created: "2026-07-04"
updated: "2026-07-04"
depends_on: []
spec_refs:
    - 7.2 The loop
---

## Description
gollama's HTTP transport retries 429/503/529 internally — 5 attempts, 5s→80s exponential backoff (~2.5 min of `time.Sleep` worst case) — before its error ever reaches ycc's loop-level retry. During that window there is no ctx cancellation (a stopped session blocks) and no visibility (the loop's transient `retry` events only cover the loop-level ring, so the UI shows nothing while gollama sleeps).

Since gollama is our library, fix it at the source: add a way to disable/configure the transport retry (or accept a ctx / retry callback), then have ycc disable the inner ring and rely solely on the loop-level retry (which is ctx-aware and broadcasts transient `retry` events), or wire the callback into the emitter.

## Acceptance criteria
- A rate-limited request is cancellable via session stop within one backoff step.
- All retry waits are visible to live subscribers (transient `retry` events).
- No double-stacked retry rings (a persistent 429 fails within one policy's budget).
- Requires a gollama bump; note the version in the work log.

## Acceptance criteria

## Work log
