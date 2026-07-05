---
id: "0133"
title: Make LLM retry policy configurable (config → Loop.Retry plumbing)
status: todo
priority: 4
created: "2026-07-04"
updated: "2026-07-04"
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

## Work log
