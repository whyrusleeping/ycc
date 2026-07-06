package config

import (
	"sort"
	"strings"
	"sync"
)

// Built-in default pricing (spec §20.4) for well-known Anthropic and OpenAI
// models, in US dollars per million tokens. Explicit per-model pricing in
// config always wins (see Model.EffectivePricing); this table only fills the
// gap so cost estimates work out of the box instead of showing "—" for the
// most common backends. Rates are estimates hard-coded from the vendors'
// published price lists and can drift when vendors change prices — configure
// price_* in [models.X] to override.
//
// Sources (fetched 2026-07):
//   - https://platform.claude.com/docs/en/about-claude/pricing
//   - https://developers.openai.com/api/docs/pricing
//
// Semantics per backend:
//   - Anthropic reports cache reads/writes DISJOINT from input_tokens, and
//     charges cache writes at a premium. CacheWrite here uses the default
//     5-minute cache-write rate (1.25× input); 1-hour caching (2× input) would
//     be slightly underestimated.
//   - OpenAI has no cache-write surcharge (CacheWrite rate 0 — the write
//     tokens are just billed as normal input) and bills cached reads at the
//     discounted "cached input" rate. "-pro" models have no caching discount,
//     so their CacheRead rate equals the input rate.
//   - Long-context premiums (e.g. gpt-5.5 >272K, Sonnet legacy 1M pricing)
//     and priority/batch tiers are NOT modeled; standard-tier short-context
//     rates are used.

// defaultPrice is one row of the built-in table: rates for a normalized
// model-id prefix.
type defaultPrice struct {
	prefix     string
	input      float64
	output     float64
	cacheRead  float64
	cacheWrite float64
}

// anthropicDefaults maps normalized Anthropic model-id prefixes to rates.
// Ordering here is cosmetic; lookup sorts by prefix length (longest first) so
// e.g. "claude-opus-4-5" wins over "claude-opus-4".
var anthropicDefaults = []defaultPrice{
	// Current flagship line.
	{"claude-fable-5", 10, 50, 1, 12.50},
	{"claude-mythos-5", 10, 50, 1, 12.50},
	{"claude-opus-4-8", 5, 25, 0.50, 6.25},
	{"claude-opus-4-7", 5, 25, 0.50, 6.25},
	{"claude-opus-4-6", 5, 25, 0.50, 6.25},
	{"claude-opus-4-5", 5, 25, 0.50, 6.25},
	// Sonnet 5 introductory pricing runs through 2026-08-31; from 2026-09-01
	// it moves to 3/15 (0.30 read, 3.75 write) — update then.
	{"claude-sonnet-5", 2, 10, 0.20, 2.50},
	{"claude-sonnet-4-6", 3, 15, 0.30, 3.75},
	{"claude-sonnet-4-5", 3, 15, 0.30, 3.75},
	{"claude-haiku-4-5", 1, 5, 0.10, 1.25},
	// Older / deprecated generations.
	{"claude-opus-4-1", 15, 75, 1.50, 18.75},
	{"claude-opus-4", 15, 75, 1.50, 18.75},
	{"claude-sonnet-4", 3, 15, 0.30, 3.75},
	{"claude-3-7-sonnet", 3, 15, 0.30, 3.75},
	{"claude-3-5-sonnet", 3, 15, 0.30, 3.75},
	{"claude-3-5-haiku", 0.80, 4, 0.08, 1},
	{"claude-3-opus", 15, 75, 1.50, 18.75},
	{"claude-3-haiku", 0.25, 1.25, 0.03, 0.30},
}

// openaiDefaults maps normalized OpenAI model-id prefixes to rates. Keys are
// normalized with dots → dashes (so "gpt-5.2" is stored as "gpt-5-2"); the
// boundary check in matchDefault keeps "gpt-5-2" from swallowing date-stamped
// ids like "gpt-5-2025-08-07".
var openaiDefaults = []defaultPrice{
	{"gpt-5-5-pro", 30, 180, 30, 0},
	{"gpt-5-5", 5, 30, 0.50, 0},
	{"gpt-5-4-mini", 0.75, 4.5, 0.075, 0},
	{"gpt-5-4-nano", 0.20, 1.25, 0.02, 0},
	{"gpt-5-4-pro", 30, 180, 30, 0},
	{"gpt-5-4", 2.5, 15, 0.25, 0},
	{"gpt-5-2-pro", 21, 168, 21, 0},
	{"gpt-5-2", 1.75, 14, 0.175, 0},
	{"gpt-5-1", 1.25, 10, 0.125, 0},
	{"gpt-5-pro", 15, 120, 15, 0},
	{"gpt-5-mini", 0.25, 2, 0.025, 0},
	{"gpt-5-nano", 0.05, 0.40, 0.005, 0},
	{"gpt-5", 1.25, 10, 0.125, 0},
	{"gpt-4-1-mini", 0.40, 1.6, 0.10, 0},
	{"gpt-4-1-nano", 0.10, 0.40, 0.025, 0},
	{"gpt-4-1", 2, 8, 0.50, 0},
	{"gpt-4o-mini", 0.15, 0.60, 0.075, 0},
	{"gpt-4o", 2.5, 10, 1.25, 0},
	{"o1-pro", 150, 600, 150, 0},
	{"o1-mini", 1.1, 4.4, 0.55, 0},
	{"o1", 15, 60, 7.5, 0},
	{"o3-pro", 20, 80, 20, 0},
	{"o3-mini", 1.1, 4.4, 0.55, 0},
	{"o3", 2, 8, 0.50, 0},
	{"o4-mini", 1.1, 4.4, 0.275, 0},
}

var sortDefaultsOnce sync.Once

// sortedDefaults returns the table for a backend family with entries sorted
// longest-prefix-first so the most specific entry wins.
func sortedDefaults(backend string) []defaultPrice {
	sortDefaultsOnce.Do(func() {
		byLen := func(t []defaultPrice) {
			sort.SliceStable(t, func(i, j int) bool {
				if len(t[i].prefix) != len(t[j].prefix) {
					return len(t[i].prefix) > len(t[j].prefix)
				}
				return t[i].prefix < t[j].prefix
			})
		}
		byLen(anthropicDefaults)
		byLen(openaiDefaults)
	})
	switch backend {
	case "anthropic":
		return anthropicDefaults
	case "openai", "openai-compatible":
		// openai-compatible endpoints only match when the id itself is a
		// well-known OpenAI/Anthropic model name, in which case the first-party
		// rate is the best available estimate.
		return openaiDefaults
	}
	return nil
}

// normalizeModelID lowercases a backend model id, maps dots to dashes
// ("gpt-4.1" → "gpt-4-1"), and strips a router-style vendor path prefix
// ("openai/gpt-5.1" → "gpt-5-1") so prefix matching works across id styles.
func normalizeModelID(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	return strings.ReplaceAll(id, ".", "-")
}

// DefaultPricing returns the built-in fallback pricing for a backend model id
// (e.g. ("anthropic", "claude-sonnet-4-5-20250929")), and whether the id
// matched a known model. Matching is longest-prefix on the normalized id with
// a boundary check: the prefix must be followed by end-of-string or "-", so
// "gpt-5-2" (gpt-5.2) matches "gpt-5.2-pro-ish" ids but not the date-stamped
// "gpt-5-2025-08-07" (which falls through to "gpt-5").
func DefaultPricing(backend, modelID string) (Pricing, bool) {
	table := sortedDefaults(backend)
	if len(table) == 0 || modelID == "" {
		return Pricing{}, false
	}
	id := normalizeModelID(modelID)
	for _, d := range table {
		if !strings.HasPrefix(id, d.prefix) {
			continue
		}
		if len(id) > len(d.prefix) && id[len(d.prefix)] != '-' {
			continue
		}
		return Pricing{
			Input:      d.input,
			Output:     d.output,
			CacheRead:  d.cacheRead,
			CacheWrite: d.cacheWrite,
			Configured: true,
		}, true
	}
	return Pricing{}, false
}
