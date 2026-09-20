---
id: "0395"
title: Known context window for gpt-6 family + warn on models with unknown window
status: proposed
priority: 3
created: "2026-09-20"
updated: "2026-09-20"
depends_on: []
spec_refs: []
---

## Description
## Problem
`knownContextWindow` (internal/config/config.go) recognizes only `gpt-5*` (400K) and o1/o3/o4 for the openai backend. `gpt-6-astra` resolves to window=0 ("unknown"), which disables every context-pressure decision and reports `context_window: 0` in `model_turn` telemetry. In valstore session `s_bd32b8b81561efc0` this let the coordinator grow to ~894K tokens.

## Proposal
- Add a `gpt-6` family entry (confirm the published window; if it is ~1M, still consider a lower *safe* default for cost since input above ~400K is rarely useful).
- `ycc doctor` / startup: warn when a configured model has no known or configured `context_window`, pointing at the `context_window` ycc.toml key.
- Short-term workaround (no code): set `context_window = 400000` (or similar) under `[models.astra]` in ~/.config/ycc/ycc.toml.

## Acceptance
- config_test covers gpt-6 prefix.
- doctor warns for unknown-window models.

## Acceptance criteria

## Work log
