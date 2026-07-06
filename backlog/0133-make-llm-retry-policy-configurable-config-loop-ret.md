---
id: "0133"
title: Make LLM retry policy configurable (config → Loop.Retry plumbing)
status: done
priority: 4
created: "2026-07-04"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 7.2 The loop
---

## Description
Retry of transient LLM API failures now lives in the engine loop (`Loop.Retry`, default `engine.DefaultRetryPolicy()`: 3 attempts, 500ms→30s equal-jitter backoff). The policy is a Loop field but nothing sets it — every loop uses the default.

Plumb an optional `[retry]` config block (e.g. `max_attempts`, `base_delay_ms`, `max_delay_ms`) through `config.Registry` into the loops built by `Session.buildLoop` and `orchestrator.Deps.newLoop` (add a `Retry` field to `orchestrator.Deps` beside `MaxTok`/`MaxTurns`).

## Acceptance criteria
- Config block parses and validates (MaxAttempts >= 1, delays > 0), absent block keeps today's defaults.
- Coordinator AND subagent loops honor the configured policy.
- `max_attempts = 1` disables retry entirely.
- Tests cover parse + plumbing.

## Acceptance criteria

## Plan

Plumb an optional [retry] TOML block through config.Registry into every engine loop.

1. internal/config/config.go
   - New `Retry` struct: `MaxAttempts int` (toml max_attempts), `BaseDelayMS int` (toml base_delay_ms), `MaxDelayMS int` (toml max_delay_ms), all `omitempty`. Semantics: 0 = unset → keep engine default for that field; max_attempts = 1 disables retry entirely.
   - Add `Retry Retry `toml:"retry,omitempty"`` to Config (beside Budget/GC).
   - validate(): reject negative values for any of the three fields; reject max_delay_ms < base_delay_ms when BOTH are set (nonzero).
   - New `Registry.RetryPolicy() engine.RetryPolicy` (config already imports engine): start from engine.DefaultRetryPolicy(), overlay each nonzero config field (delays converted from ms). Guard with r.mu.RLock like MaxTokens/MaxTurns. Because the result always has MaxAttempts >= 1, the loop's "zero value => default" fallback never kicks in and max_attempts=1 truly disables retry.

2. internal/orchestrator/orchestrator.go
   - Add `Retry engine.RetryPolicy` field to Deps beside MaxTok/MaxTurns; set `Retry: d.Retry` in Deps.newLoop so implementer/reviewer (subagent) loops honor it.

3. internal/orchestrator/capture.go
   - Add `Retry engine.RetryPolicy` to CaptureDeps and set it on the capture loop, for consistency.

4. internal/session/session.go
   - newSession: set `Retry: m.reg.RetryPolicy()` in the orchestrator.Deps literal; also set `Retry` on the coordinator loop built in s.buildLoop.
   - Manager capture path (~line 1865, CaptureDeps literal): set `Retry: m.reg.RetryPolicy()`.

5. Tests
   - config tests: TOML with [retry] parses; absent block → RetryPolicy() == engine.DefaultRetryPolicy(); partial block overlays only set fields; max_attempts=1 yields MaxAttempts 1; negative values and max_delay < base_delay rejected by validate; Save/Load round-trip keeps the block.
   - plumbing tests: follow existing MaxTurns-plumbing test patterns — assert Deps.newLoop copies Deps.Retry onto the loop, and session buildLoop / newSession put the configured policy on coordinator + deps (whatever level existing session tests reach).

6. spec.md
   - Config example (§ around lines 755–767): add a commented `[retry]` block beside max_tokens/max_turns.
   - §7.2 retry paragraph: note the policy is configurable via [retry] (max_attempts/base_delay_ms/max_delay_ms), absent = today's default, max_attempts=1 disables.

Verification: go build ./... && go test ./... (or at least ./internal/config ./internal/orchestrator ./internal/session ./internal/engine).

### Starting points
- internal/engine/retry.go — RetryPolicy + DefaultRetryPolicy (3 attempts, 500ms→30s)
- internal/engine/loop.go:434 — runTurn falls back to default when policy.MaxAttempts == 0
- internal/config/config.go:289-321 Config struct; :435 validate(); :509-522 Registry.MaxTokens/MaxTurns pattern (config already imports internal/engine for Thinking)
- internal/orchestrator/orchestrator.go:87-119 Deps struct; :192-207 newLoop
- internal/orchestrator/capture.go:28 CaptureDeps, :127 capture loop literal
- internal/session/session.go:1315-1327 Deps literal; :1356-1379 buildLoop; :1865-1874 CaptureDeps literal
- spec.md:755-767 config example; spec.md:406-422 §7.2 retry paragraph

## Work log
- 2026-07-06 plan: Plumb an optional [retry] TOML block through config.Registry into every engine loop.  1. internal/config/config.go    - New `Retry` struct: `MaxAttempts int` (toml max_attempts), `BaseDelayMS int` (to
…[truncated]
- 2026-07-06 context hints: 7 recorded with plan
- 2026-07-06 context hints: internal/engine/retry.go — RetryPolicy + DefaultRetryPolicy (3 attempts, 500ms→30s); internal/engine/loop.go:434 — runTurn: policy.MaxAttempts == 0 => DefaultRetryPolicy(); internal/config/confi
…[truncated]
- 2026-07-06 implementer report: Implemented Task 0133: LLM retry policy is now configurable via an optional [retry] TOML block plumbed through config.Registry into every engine loop.  Changes: - internal/config/config.go   - Added `
…[truncated]
- 2026-07-06 review tier: single-opus — reviewers: Claude
- 2026-07-06 review (Claude): accept — The change plumbs an optional [retry] TOML block through config.Registry.RetryPolicy() into every engine loop (coordinator via buildLoop + newSession Deps, subagents via Deps.newLoop, capture via Capt
…[truncated]
- 2026-07-06 decision: accept — commit: config: make LLM retry policy configurable via [retry] block (task 0133)  Plumb an optional [retry] TOML block (max_attempts / base_delay_ms / max_delay_ms) through config.Registry.RetryPolicy() into 
…[truncated]
- 2026-07-06 usage: 20,429 tok (in 146, out 20,283, cache_r 2,380,604, cache_w 89,182) · cost n/a (unpriced)
  implementer: 15,812 tok (in 116, out 15,696, cache_r 1,928,203, cache_w 52,518) · cost n/a (unpriced)
  coordinator: 3,054 tok (in 16, out 3,038, cache_r 383,827, cache_w 17,583) · cost n/a (unpriced)
  reviewer:Claude: 1,563 tok (in 14, out 1,549, cache_r 68,574, cache_w 19,081) · cost n/a (unpriced)
