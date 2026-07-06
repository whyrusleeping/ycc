---
id: "0134"
title: Surface/limit gollama's internal transport retries (uncancellable, invisible)
status: done
priority: 4
created: "2026-07-04"
updated: "2026-07-06"
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

## Plan

Goal: eliminate gollama's invisible, uncancellable inner retry ring (429/503/529, 5 attempts, 5s→80s time.Sleep) and rely solely on ycc's loop-level retry (ctx-aware, broadcasts transient `retry` events).

Part A — gollama (sibling repo ~/code/gollama, currently at 567eebc = the version pinned in ycc's go.mod):
1. Add a configurable transport retry knob on `Client`: a `maxRetries *int` field (nil ⇒ current default of 5, preserving behavior for all existing users) plus a `SetMaxRetries(n int)` setter documented as "n = number of retries after the initial attempt; 0 disables transport-level retry entirely". `doWithRetry` reads the effective value instead of the `maxRetries` package const.
2. Verify nothing else constructs `Client` outside `NewClient` (rg `&Client{`) and that bedrock doesn't route through doWithRetry; keep the change additive and non-breaking.
3. `go build ./... && go vet ./...` and run the non-live tests (`go test ./... -short` or skip *_live_test via missing API keys).
4. Commit in gollama with a clear message and push to origin main (established pattern from tasks 0001/0025/0093). Record the new sha.

Part B — ycc (this workspace):
5. Bump go.mod: `GOPRIVATE=github.com/whyrusleeping go get github.com/whyrusleeping/gollama@<new-sha>` then `go mod tidy` (GOPRIVATE avoids proxy.golang.org lag right after push).
6. `internal/config/config.go` Registry.Build: call `c.SetMaxRetries(0)` on the freshly built client, with a comment explaining the single ctx-aware retry ring lives in engine.Loop.runTurn (spec §7.2); update the existing trailing comment there.
7. Compensate for losing the inner ring's rate-limit tolerance by raising the loop default: `engine.DefaultRetryPolicy()` → MaxAttempts 8 (was 3), keeping BaseDelay 500ms / MaxDelay 30s. Worst-case budget ≈ 60s of jittered backoff — between the old loop-only ~2s and the old stacked ~2.5min+, and still fully configurable via the `[retry]` config block. Update the doc comments in internal/engine/retry.go (delete/replace the now-false "Layering note" about gollama's inner ring) and internal/config/config.go:323.
8. Update spec.md: §7.2 API-failure-handling text (default "3 total attempts" → 8; rewrite the layering note to say ycc disables gollama's transport retry so the loop ring is the only one) and the config sample comment near line 773 ("3 attempts, 500ms→30s" → "8 attempts, 500ms→30s").
9. Check retry_test.go tests using DefaultRetryPolicy() (they use fake sleep seams; adjust any that assume 3 attempts) and config_test.go (compares symbolically to DefaultRetryPolicy(), should be fine). `go build ./... && go test ./...`.

Acceptance mapping: with the inner ring disabled, every backoff wait happens in Loop.runTurn — ctx-cancellable (session stop aborts within one step) and each wait broadcasts a transient `retry` event; a persistent 429 fails within the single loop policy's budget. gollama bump sha to be noted in the work log.

Leave cmd/ycc-spike untouched (standalone tool, default gollama behavior fine).

### Starting points
- ~/code/gollama/http.go — doWithRetry + maxRetries/baseDelay consts (the inner ring)
- ~/code/gollama/client.go — Client struct + NewClient + setters
- internal/config/config.go Registry.Build (~line 959) — where the gollama client is constructed per call; trailing comment about retry layering
- internal/engine/retry.go — RetryPolicy, DefaultRetryPolicy, stale layering note
- internal/engine/loop.go runTurn (~line 433) — the ctx-aware loop retry ring + transient retry events
- spec.md lines ~406-435 (API failure handling) and ~773 ([retry] config sample)
- internal/engine/retry_test.go, internal/config/config_test.go (~line 868)

## Work log
- 2026-07-06 plan: Goal: eliminate gollama's invisible, uncancellable inner retry ring (429/503/529, 5 attempts, 5s→80s time.Sleep) and rely solely on ycc's loop-level retry (ctx-aware, broadcasts transient `retry` ev
…[truncated]
- 2026-07-06 context hints: 7 recorded with plan
- 2026-07-06 context hints: ~/code/gollama/http.go — doWithRetry + maxRetries/baseDelay consts (the inner ring); ~/code/gollama/client.go — Client struct + NewClient + setters; internal/config/config.go Registry.Build (~line
…[truncated]
- 2026-07-06 implementer report: Implemented Task 0134 — eliminated gollama's invisible, uncancellable inner retry ring and made ycc's ctx-aware loop-level retry the sole ring.  ## Part A — gollama (~/code/gollama) - client.go: a
…[truncated]
- 2026-07-06 review tier: single-opus — reviewers: Claude
- 2026-07-06 review (Claude): accept — The change correctly eliminates gollama's invisible/uncancellable inner transport retry ring. gollama is bumped to a version adding SetMaxRetries; ycc calls SetMaxRetries(0) in config.Registry.Build, 
…[truncated]
- 2026-07-06 decision: accept — commit: retry: disable gollama's transport retry ring; single ctx-aware loop retry (task 0134)  gollama d8e738f adds Client.SetMaxRetries; ycc bumps to v0.0.0-20260706030410-d8e738f47e06, disables the inner r
…[truncated]
