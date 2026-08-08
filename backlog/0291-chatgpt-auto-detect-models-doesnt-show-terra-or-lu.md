---
id: "0291"
title: Chatgpt auto-detect models doesnt show terra or luna models for some reason
status: done
priority: 3
created: "2026-08-07"
updated: "2026-08-08"
depends_on: []
spec_refs: []
---

## Description

## Acceptance criteria

## Plan

ROOT CAUSE (investigated + verified live): "ChatGPT auto-detect" for the codex backend has no listing endpoint, so DiscoverConnModels returns the curated `internal/codex.Models` list — which predates the GPT-5.6 family GA (2026-07-09) and only carries gpt-5.6-sol. The terra/luna tiers (gpt-5.6-terra, gpt-5.6-luna) are missing. I verified live against the real ChatGPT codex backend (OAuth) that gpt-5.6-sol/terra/luna, gpt-5.5, gpt-5.4 and gpt-5.4-mini ALL serve successfully today.

CHANGES:
1. internal/codex/codex.go — extend the curated list, keeping sol FIRST (Models[0] is the subscription default used by config/subscription.go and the setup wizard):
   var Models = []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini"}
   Update the doc comment: verified live 2026-08; note gpt-5.4/gpt-5.4-mini are deprecated in Codex's own catalog (forced upgrade paths to terra/luna) but still serve, so they stay as tail suggestions.
2. internal/config/discover.go — curatedModelIDs["openai"]: prepend the 5.6 tiers → {"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5-mini", "gpt-4o", "o3"} (they are also platform-API models).
3. internal/config/default_pricing.go — add GPT-5.6 family rows to openaiDefaults (rates from OpenAI's published rate card, fetched 2026-08; per M tokens, input/output/cacheRead/cacheWrite):
   {"gpt-5-6-sol", 5, 30, 0.50, 6.25},
   {"gpt-5-6-terra", 2, 12, 0.20, 2.50},
   {"gpt-5-6-luna", 0.20, 1.20, 0.02, 0.25},
   {"gpt-5-6", 5, 30, 0.50, 6.25},   // bare "gpt-5.6" alias routes to Sol
   Longest-prefix-first sorting makes the tier rows win over "gpt-5-6", and "gpt-5-6" wins over "gpt-5". IMPORTANT: the 5.6 family is the first OpenAI line with a cache-WRITE surcharge (1.25× input), so update the package comment block ("OpenAI has no cache-write surcharge…") to note the 5.6 exception. Long-context (>272K) premium remains unmodeled, consistent with the existing comment.
4. Tests — extend internal/config/default_pricing_test.go: DefaultPricing("openai", …) for "gpt-5.6-sol" (5/30/0.50/6.25), "gpt-5.6-terra", "gpt-5.6-luna", bare "gpt-5.6" → Sol rates, and a boundary case (e.g. "gpt-5.6-terra-2026-01-01" still matches terra; "gpt-5.60" does NOT match "gpt-5-6"). Add/extend a test asserting codex.Models contains the three 5.6 tiers with sol first (or verify existing wizard/subscription tests, which index Models[0], still pass unchanged).
5. Verify: go build ./... && go test ./internal/codex ./internal/config ./internal/setup ./internal/tui (note: repo-wide `go test ./...` has known flaky tests elsewhere).

Non-goals: no thinking-effort ladder changes (xhigh/ultra), no long-context premium modeling, no spec change (spec.md:1166 "curated set in internal/codex.Models, e.g. gpt-5.6-sol" remains accurate).

### Starting points
- internal/codex/codex.go:42-46 — var Models (curated codex list; Models[0] is the default everywhere)
- internal/config/discover.go:19-24 — curatedModelIDs map
- internal/config/default_pricing.go:72-102 — openaiDefaults table; matchDefault boundary semantics at 144-172
- internal/config/subscription.go + internal/setup/wizard.go use codex.Models[0] as the default model id — order matters
- pricing source: OpenAI rate card — sol $5/$30 (cache read $0.50, write $6.25), terra $2/$12 ($0.20/$2.50), luna $0.20/$1.20 ($0.02/$0.25)

## Work log
- 2026-08-08 plan: ROOT CAUSE (investigated + verified live): "ChatGPT auto-detect" for the codex backend has no listing endpoint, so DiscoverConnModels returns the curated `internal/codex.Models` list — which predate
…[truncated]
- 2026-08-08 context hints: 5 recorded with plan
- 2026-08-08 context hints: internal/codex/codex.go:42-46 — var Models; internal/config/discover.go:19-24 — curatedModelIDs map; internal/config/default_pricing.go — openaiDefaults table + comment block lines 21-32; Defaul
…[truncated]
- 2026-08-08 preload: 4 file(s), ~16 KiB seeded into implementer context
- 2026-08-08 implementer report: Implemented task 0291: - Expanded `internal/codex.Models` with `gpt-5.6-terra` and `gpt-5.6-luna`, retaining `gpt-5.6-sol` first; documented live-verification date and deprecated-but-serving 5.4 tail 
…[truncated]
- 2026-08-08 review tier: simple (coordinator self-review)
