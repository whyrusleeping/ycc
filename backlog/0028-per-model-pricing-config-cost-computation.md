---
id: "0028"
title: Per-model pricing config + cost computation
status: todo
priority: 3
created: "2026-06-26"
updated: "2026-06-26"
depends_on: []
spec_refs:
    - Token usage & cost accounting
    - Backends & model registry
---

## Description
Add optional per-model pricing to config and a cost computation helper (spec §20.4) so
token usage can be turned into dollar cost. Pricing lives in config (not code) so it can
be updated as vendor prices change without touching the event log.

## Context
- `internal/config/config.go` `Model` struct holds the per-`[models.X]` block.
- Prices differ by token class (fresh input, output, cache-read, cache-write).

## Acceptance criteria
- [ ] `Model` gains optional pricing fields in US dollars per million tokens:
      `price_input`, `price_output`, `price_cache_read`, `price_cache_write`
      (TOML keys), all defaulting to 0/unset.
- [ ] Registry exposes pricing for a logical model name (e.g. `PricingFor(name)`).
- [ ] A cost helper computes turn/aggregate cost = Σ(tokens_class × rate_class), taking a
      usage breakdown + pricing and returning a dollar amount, with a clear "unpriced"
      signal when no pricing is configured (cost reported as unknown, not 0).
- [ ] Unit tests for the cost math (including unpriced and cache-token cases).
- [ ] `config.Save` (task 0022) round-trips the new fields if it has landed; otherwise
      note the follow-up.

## Acceptance criteria

## Work log
