package config

import "testing"

func TestDefaultPricingAnthropic(t *testing.T) {
	cases := []struct {
		id          string
		in, out     float64
		read, write float64
	}{
		// Date-stamped ids match their family prefix.
		{"claude-sonnet-4-5-20250929", 3, 15, 0.30, 3.75},
		{"claude-haiku-4-5", 1, 5, 0.10, 1.25},
		{"claude-opus-4-5-20251101", 5, 25, 0.50, 6.25},
		// "claude-opus-4-1" must win over the shorter "claude-opus-4".
		{"claude-opus-4-1-20250805", 15, 75, 1.50, 18.75},
		// Bare Opus 4 (date suffix crosses the boundary check cleanly).
		{"claude-opus-4-20250514", 15, 75, 1.50, 18.75},
		// -latest aliases.
		{"claude-3-5-haiku-latest", 0.80, 4, 0.08, 1},
		// Dotted / vendor-prefixed router ids normalize.
		{"anthropic/claude-sonnet-4.5", 3, 15, 0.30, 3.75},
	}
	for _, tc := range cases {
		p, ok := DefaultPricing("anthropic", tc.id)
		if !ok {
			t.Errorf("%s: no default pricing", tc.id)
			continue
		}
		if p.Input != tc.in || p.Output != tc.out || p.CacheRead != tc.read || p.CacheWrite != tc.write {
			t.Errorf("%s: got in=%v out=%v read=%v write=%v, want %v/%v/%v/%v",
				tc.id, p.Input, p.Output, p.CacheRead, p.CacheWrite, tc.in, tc.out, tc.read, tc.write)
		}
	}
}

func TestDefaultPricingOpenAI(t *testing.T) {
	cases := []struct {
		id      string
		in, out float64
		read    float64
	}{
		{"gpt-5.1", 1.25, 10, 0.125},
		{"gpt-5", 1.25, 10, 0.125},
		// Date-stamped gpt-5 id must NOT match "gpt-5-2" (gpt-5.2): the
		// boundary check rejects "gpt-5-2" + "025…" and falls through to "gpt-5".
		{"gpt-5-2025-08-07", 1.25, 10, 0.125},
		{"gpt-5.2", 1.75, 14, 0.175},
		{"gpt-5-mini", 0.25, 2, 0.025},
		{"gpt-5-nano", 0.05, 0.40, 0.005},
		{"gpt-4.1-mini", 0.40, 1.6, 0.10},
		{"gpt-4o-mini", 0.15, 0.60, 0.075},
		{"o4-mini", 1.1, 4.4, 0.275},
		{"o3", 2, 8, 0.50},
	}
	for _, tc := range cases {
		p, ok := DefaultPricing("openai", tc.id)
		if !ok {
			t.Errorf("%s: no default pricing", tc.id)
			continue
		}
		if p.Input != tc.in || p.Output != tc.out || p.CacheRead != tc.read {
			t.Errorf("%s: got in=%v out=%v read=%v, want %v/%v/%v",
				tc.id, p.Input, p.Output, p.CacheRead, tc.in, tc.out, tc.read)
		}
		if p.CacheWrite != 0 {
			t.Errorf("%s: OpenAI cache-write rate should be 0, got %v", tc.id, p.CacheWrite)
		}
	}
}

func TestDefaultPricingUnknown(t *testing.T) {
	for _, tc := range []struct{ backend, id string }{
		{"anthropic", "claude-9-hyperion"},
		{"openai", "gpt-3.5-turbo"}, // legacy, intentionally not in the table
		{"openai-compatible", "llama-3.3-70b-instruct"},
		{"ollama", "gpt-5.1"}, // local backends never get vendor rates
		{"glm", "glm-4.7"},
		{"anthropic", ""},
	} {
		if p, ok := DefaultPricing(tc.backend, tc.id); ok || p.Configured {
			t.Errorf("(%s, %s): expected no default pricing, got %+v", tc.backend, tc.id, p)
		}
	}
}

// Config pricing must always win over the built-in defaults.
func TestEffectivePricingConfigWins(t *testing.T) {
	m := Model{
		Backend:    "anthropic",
		Model:      "claude-sonnet-4-5",
		PriceInput: fp(99),
	}
	p := m.EffectivePricing()
	if !p.Configured || p.Input != 99 {
		t.Fatalf("config pricing should win: %+v", p)
	}
	// Config pricing replaces the defaults entirely — no field-level merge.
	if p.Output != 0 {
		t.Fatalf("partial config pricing should not merge defaults: %+v", p)
	}
}

func TestEffectivePricingFallsBackToDefaults(t *testing.T) {
	m := Model{Backend: "anthropic", Model: "claude-sonnet-4-5-20250929"}
	p := m.EffectivePricing()
	if !p.Configured || p.Input != 3 || p.Output != 15 {
		t.Fatalf("expected built-in sonnet rates, got %+v", p)
	}
}

func TestRegistryPricingForUsesDefaults(t *testing.T) {
	r := NewRegistry(newCfg(Model{Backend: "anthropic", Model: "claude-haiku-4-5"}))
	p := r.PricingFor("claude")
	if !p.Configured || p.Input != 1 || p.Output != 5 {
		t.Fatalf("expected built-in haiku rates via registry, got %+v", p)
	}
}
