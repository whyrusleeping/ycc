package config

import (
	"path/filepath"
	"testing"

	"github.com/whyrusleeping/ycc/internal/event"
)

func fp(v float64) *float64 { return &v }

func newCfg(m Model) *Config {
	return &Config{
		Models: map[string]Model{"claude": m},
		Roles:  Roles{Coordinator: "claude", Implementer: "claude", Reviewers: []string{"claude"}},
	}
}

func TestPricingCostFullyPriced(t *testing.T) {
	// Rates in $/Mtok chosen so the arithmetic is easy to verify.
	m := Model{
		Backend:         "anthropic",
		PriceInput:      fp(3),  // $3 / Mtok
		PriceOutput:     fp(15), // $15 / Mtok
		PriceCacheRead:  fp(1),  // $1 / Mtok
		PriceCacheWrite: fp(4),  // $4 / Mtok
	}
	r := NewRegistry(newCfg(m))
	p := r.PricingFor("claude")
	if !p.Configured {
		t.Fatalf("expected configured pricing")
	}
	u := event.Usage{Input: 1_000_000, Output: 2_000_000, CacheRead: 3_000_000, CacheWrite: 4_000_000}
	// 1*3 + 2*15 + 3*1 + 4*4 = 3 + 30 + 3 + 16 = 52
	cost, priced := p.Cost(u)
	if !priced {
		t.Fatalf("expected priced=true")
	}
	if cost != 52 {
		t.Fatalf("cost = %v, want 52", cost)
	}
}

func TestPricingUnpriced(t *testing.T) {
	r := NewRegistry(newCfg(Model{Backend: "anthropic"}))
	p := r.PricingFor("claude")
	if p.Configured {
		t.Fatalf("expected unconfigured pricing")
	}
	cost, priced := p.Cost(event.Usage{Input: 1_000_000, Output: 1_000_000})
	if priced {
		t.Fatalf("expected priced=false for unpriced model")
	}
	if cost != 0 {
		t.Fatalf("unpriced cost should be 0 placeholder, got %v", cost)
	}
}

func TestPricingUnknownModel(t *testing.T) {
	r := NewRegistry(newCfg(Model{Backend: "anthropic", PriceInput: fp(3)}))
	if p := r.PricingFor("nope"); p.Configured {
		t.Fatalf("unknown model should be unconfigured")
	}
}

func TestPricingPartial(t *testing.T) {
	m := Model{Backend: "anthropic", PriceInput: fp(3), PriceOutput: fp(15)}
	p := m.Pricing()
	if !p.Configured {
		t.Fatalf("expected configured pricing")
	}
	if p.CacheRead != 0 || p.CacheWrite != 0 {
		t.Fatalf("unset cache rates should be 0, got read=%v write=%v", p.CacheRead, p.CacheWrite)
	}
	// Cache tokens present but unpriced contribute 0.
	u := event.Usage{Input: 1_000_000, Output: 1_000_000, CacheRead: 5_000_000, CacheWrite: 5_000_000}
	// 1*3 + 1*15 + 5*0 + 5*0 = 18
	cost, priced := p.Cost(u)
	if !priced || cost != 18 {
		t.Fatalf("cost = %v priced=%v, want 18 true", cost, priced)
	}
}

func TestPricingRoundTrip(t *testing.T) {
	m := Model{
		Backend:         "anthropic",
		Model:           "claude-x",
		PriceInput:      fp(3),
		PriceOutput:     fp(15),
		PriceCacheRead:  fp(0.30),
		PriceCacheWrite: fp(3.75),
	}
	c := newCfg(m)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Save(path, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	gm := got.Models["claude"]
	for _, tc := range []struct {
		name string
		got  *float64
		want float64
	}{
		{"price_input", gm.PriceInput, 3},
		{"price_output", gm.PriceOutput, 15},
		{"price_cache_read", gm.PriceCacheRead, 0.30},
		{"price_cache_write", gm.PriceCacheWrite, 3.75},
	} {
		if tc.got == nil {
			t.Fatalf("%s did not round-trip (nil)", tc.name)
		}
		if *tc.got != tc.want {
			t.Fatalf("%s = %v, want %v", tc.name, *tc.got, tc.want)
		}
	}
}
