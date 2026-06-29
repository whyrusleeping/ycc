package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/secrets"
)

const sample = `
max_tokens = 4096
max_turns = 250

[models.claude]
backend = "anthropic"
base_url = "https://api.anthropic.com"
model = "claude-opus-4-8"
key_env = "ANTHROPIC_API_KEY"
effort = "max"
thinking_display = "summarized"

[models.haiku]
backend = "anthropic"
base_url = "https://api.anthropic.com"
model = "claude-haiku-4-5"
key_env = "ANTHROPIC_API_KEY"
thinking = "off"

[models.local]
backend = "ollama"
base_url = "http://localhost:11434/v1"
model = "qwen2.5-coder"

[roles]
coordinator = "claude"
implementer = "claude"
reviewers = ["claude", "haiku", "local"]
`

func TestLoadAndRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ycc.toml")
	if err := os.WriteFile(path, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxTokens != 4096 {
		t.Fatalf("max_tokens = %d", cfg.MaxTokens)
	}
	if cfg.MaxTurns != 250 {
		t.Fatalf("max_turns = %d", cfg.MaxTurns)
	}
	if NewRegistry(cfg).MaxTurns() != 250 {
		t.Fatalf("registry MaxTurns = %d", NewRegistry(cfg).MaxTurns())
	}
	if len(cfg.Roles.Reviewers) != 3 {
		t.Fatalf("reviewers = %v", cfg.Roles.Reviewers)
	}

	reg := NewRegistry(cfg)
	if reg.ImplementerName() != "claude" {
		t.Fatalf("implementer = %s", reg.ImplementerName())
	}
	// ollama backend builds without a key.
	c, model, err := reg.Build("local")
	if err != nil || c == nil || model != "qwen2.5-coder" {
		t.Fatalf("build local: %v model=%s", err, model)
	}
	// anthropic backend builds and returns the right model id.
	if _, model, err := reg.Build("haiku"); err != nil || model != "claude-haiku-4-5" {
		t.Fatalf("build haiku: %v model=%s", err, model)
	}
}

func TestThinkingParsingAndDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ycc.toml")
	if err := os.WriteFile(path, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(cfg)

	// claude: thinking unset -> default "adaptive"; effort explicitly "max";
	// display explicitly "summarized".
	c := reg.ThinkingFor("claude")
	if c.Thinking != "adaptive" || c.Effort != "max" || c.ThinkingDisplay != "summarized" {
		t.Fatalf("claude thinking = %+v", c)
	}

	// haiku: thinking = "off" disables reasoning entirely (zero value).
	h := reg.ThinkingFor("haiku")
	if h.Thinking != "" || h.Effort != "" || h.ThinkingDisplay != "" {
		t.Fatalf("haiku thinking should be disabled, got %+v", h)
	}

	// local: nothing set -> full reasoning-on defaults.
	l := reg.ThinkingFor("local")
	if l.Thinking != "adaptive" || l.Effort != "high" || l.ThinkingDisplay != "summarized" {
		t.Fatalf("local thinking defaults = %+v", l)
	}

	// Unknown name falls back to defaults rather than empty.
	u := reg.ThinkingFor("nope")
	if u.Thinking != "adaptive" || u.Effort != "high" {
		t.Fatalf("unknown thinking defaults = %+v", u)
	}
}

func TestDefaultAnthropicCarriesThinking(t *testing.T) {
	cfg := DefaultAnthropic("https://api.anthropic.com", "claude-opus-4-8", "ANTHROPIC_API_KEY", 8192)
	th := NewRegistry(cfg).ThinkingFor("claude")
	if th.Thinking != "adaptive" || th.Effort != "high" || th.ThinkingDisplay != "summarized" {
		t.Fatalf("default anthropic thinking = %+v", th)
	}
}

func TestModelsEnumeratesSorted(t *testing.T) {
	cfg := &Config{
		Models: map[string]Model{
			"claude": {Backend: "anthropic", Model: "claude-opus-4-8"},
			"local":  {Backend: "ollama", Model: "qwen2.5-coder"},
			"gpt":    {Backend: "openai", Model: "gpt-5.5"},
		},
		Roles: Roles{Coordinator: "claude", Implementer: "claude", Reviewers: []string{"claude"}},
	}
	reg := NewRegistry(cfg)
	got := reg.Models()
	if len(got) != 3 {
		t.Fatalf("Models() len = %d", len(got))
	}
	want := []string{"claude", "gpt", "local"} // sorted by name
	for i, m := range got {
		if m.Name != want[i] {
			t.Fatalf("Models()[%d].Name = %q, want %q", i, m.Name, want[i])
		}
	}
	if got[0].Backend != "anthropic" || got[0].Model != "claude-opus-4-8" {
		t.Fatalf("claude info = %+v", got[0])
	}
	if !reg.Has("gpt") || reg.Has("nope") {
		t.Fatal("Has() wrong")
	}
}

func TestValidateRejectsUnknownModel(t *testing.T) {
	cfg := &Config{
		Models: map[string]Model{"a": {Backend: "anthropic"}},
		Roles:  Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"missing"}},
	}
	if err := cfg.validate(); err == nil {
		t.Fatal("expected validation error for unknown reviewer model")
	}
}

func TestDefaultAnthropic(t *testing.T) {
	cfg := DefaultAnthropic("https://api.anthropic.com", "claude-opus-4-8", "ANTHROPIC_API_KEY", 8192)
	if err := cfg.validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
	if len(cfg.Roles.Reviewers) != 1 {
		t.Fatalf("default reviewers = %v", cfg.Roles.Reviewers)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	// A nested, not-yet-existing directory exercises MkdirAll.
	path := filepath.Join(t.TempDir(), "nested", "deeper", "ycc.toml")
	orig := &Config{
		Models: map[string]Model{
			"claude": {
				Backend: "anthropic", BaseURL: "https://api.anthropic.com",
				Model: "claude-opus-4-8", KeyEnv: "ANTHROPIC_API_KEY",
				Effort: "max", ThinkingDisplay: "summarized",
			},
			"haiku": {
				Backend: "anthropic", BaseURL: "https://api.anthropic.com",
				Model: "claude-haiku-4-5", KeyEnv: "ANTHROPIC_API_KEY",
				Thinking: "off",
			},
			"local": {
				Backend: "ollama", BaseURL: "http://localhost:11434/v1",
				Model: "qwen2.5-coder",
			},
		},
		Roles:     Roles{Coordinator: "claude", Implementer: "claude", Reviewers: []string{"claude", "haiku", "local"}},
		MaxTokens: 4096,
		MaxTurns:  250,
	}
	if err := Save(path, orig); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Never persist inline secret values — only key_env references.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "key_env"; !strings.Contains(string(data), want) {
		t.Fatalf("saved config missing %q reference:\n%s", want, data)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load back: %v", err)
	}
	if !reflect.DeepEqual(got, orig) {
		t.Fatalf("round-trip mismatch:\n got=%+v\nwant=%+v", got, orig)
	}
}

func TestRoleThinkingRoundTripAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ycc.toml")
	orig := &Config{
		Models: map[string]Model{
			"claude": {Backend: "anthropic", BaseURL: "u", Model: "m", KeyEnv: "K"},
		},
		Roles: Roles{
			Coordinator: "claude", Implementer: "claude", Reviewers: []string{"claude"},
			Thinking: RoleThinking{Coordinator: "xhigh", Implementer: "low", Reviewers: "high"},
		},
	}
	if err := Save(path, orig); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, orig) {
		t.Fatalf("round-trip mismatch:\n got=%+v\nwant=%+v", got, orig)
	}

	// Registry exposes per-role overrides; unset roles report ok=false.
	reg := NewRegistry(got)
	if lvl, ok := reg.RoleThinking(RoleCoordinator); !ok || lvl != "xhigh" {
		t.Fatalf("RoleThinking(coordinator) = %q,%v", lvl, ok)
	}
	if lvl, ok := reg.RoleThinking(RoleImplementer); !ok || lvl != "low" {
		t.Fatalf("RoleThinking(implementer) = %q,%v", lvl, ok)
	}

	// An unset role falls back (ok=false).
	noOverride := NewRegistry(&Config{
		Models: map[string]Model{"claude": {Backend: "anthropic", BaseURL: "u", Model: "m"}},
		Roles:  Roles{Coordinator: "claude", Implementer: "claude", Reviewers: []string{"claude"}},
	})
	if lvl, ok := noOverride.RoleThinking(RoleReviewers); ok || lvl != "" {
		t.Fatalf("unset RoleThinking(reviewers) = %q,%v, want \"\",false", lvl, ok)
	}

	// Invalid per-role level is rejected.
	bad := &Config{
		Models: map[string]Model{"claude": {Backend: "anthropic", BaseURL: "u", Model: "m"}},
		Roles: Roles{
			Coordinator: "claude", Implementer: "claude", Reviewers: []string{"claude"},
			Thinking: RoleThinking{Coordinator: "bogus"},
		},
	}
	if err := bad.validate(); err == nil {
		t.Fatal("expected validation error for invalid per-role thinking level")
	}
}

func TestSaveRejectsInvalidConfigWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ycc.toml")

	// Role references an unknown model.
	bad := &Config{
		Models: map[string]Model{"a": {Backend: "anthropic"}},
		Roles:  Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"missing"}},
	}
	if err := Save(path, bad); err == nil {
		t.Fatal("expected Save to reject config with unknown reviewer model")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Save wrote a file for an invalid config (err=%v)", err)
	}

	// Empty reviewers list is also invalid.
	bad2 := &Config{
		Models: map[string]Model{"a": {Backend: "anthropic"}},
		Roles:  Roles{Coordinator: "a", Implementer: "a"},
	}
	if err := Save(path, bad2); err == nil {
		t.Fatal("expected Save to reject config with empty reviewers")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Save wrote a file for an invalid config (err=%v)", err)
	}
}

// baseRegistry returns a Registry with a single "claude" model referenced by
// every role, the common starting point for the runtime-mutation tests.
func baseRegistry() *Registry {
	return NewRegistry(&Config{
		Models: map[string]Model{
			"claude": {Backend: "anthropic", BaseURL: "https://api", Model: "claude-x", KeyEnv: "ANTHROPIC_API_KEY"},
		},
		Roles: Roles{Coordinator: "claude", Implementer: "claude", Reviewers: []string{"claude"}},
	})
}

func TestUpsertModelLiveAddAndEdit(t *testing.T) {
	reg := baseRegistry()

	// Add a brand-new model (live only).
	gpt := Model{Backend: "openai", BaseURL: "https://oai", Model: "gpt-4o", KeyEnv: "OPENAI_API_KEY"}
	if err := reg.UpsertModel("gpt", gpt, false); err != nil {
		t.Fatalf("UpsertModel(gpt): %v", err)
	}
	if !reg.Has("gpt") {
		t.Fatal("expected Has(gpt) after upsert")
	}
	if _, id, err := reg.Build("gpt"); err != nil || id != "gpt-4o" {
		t.Fatalf("Build(gpt) = %q,%v, want gpt-4o,nil", id, err)
	}
	if got, ok := reg.GetModel("gpt"); !ok || got.Backend != "openai" || got.KeyEnv != "OPENAI_API_KEY" {
		t.Fatalf("GetModel(gpt) = %+v,%v", got, ok)
	}

	// Editing an existing model's id is reflected by the next Build.
	gpt.Model = "gpt-4o-mini"
	if err := reg.UpsertModel("gpt", gpt, false); err != nil {
		t.Fatalf("UpsertModel(gpt) edit: %v", err)
	}
	if _, id, _ := reg.Build("gpt"); id != "gpt-4o-mini" {
		t.Fatalf("Build(gpt) after edit = %q, want gpt-4o-mini", id)
	}
}

// TestModelSiblingSharesCredentials exercises the core ergonomic of task 0042:
// running different model ids that share one provider's credentials/endpoint. A
// sibling "claude-sonnet" reuses the base "claude" backend/base_url/key_env (and
// a pricing pointer) but points at a different model id — no credential is
// re-entered. Both names resolve through Build to their own model id under the
// shared key.
func TestModelSiblingSharesCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "secret")
	reg := baseRegistry()

	base, ok := reg.GetModel("claude")
	if !ok {
		t.Fatal("expected base claude model present")
	}
	// Give the base a price so we can confirm the sibling inherits it.
	price := 3.0
	base.PriceInput = &price
	if err := reg.UpsertModel("claude", base, false); err != nil {
		t.Fatalf("UpsertModel(claude) with price: %v", err)
	}

	// Create the sibling by copying the base and changing only name + model id —
	// the credential fields (Backend/BaseURL/KeyEnv) and pricing are reused.
	sibling := base
	sibling.Model = "claude-sonnet-4-5"
	if err := reg.UpsertModel("claude-sonnet", sibling, false); err != nil {
		t.Fatalf("UpsertModel(claude-sonnet): %v", err)
	}

	// Each logical name resolves to its own model id under the shared credential.
	if _, id, err := reg.Build("claude"); err != nil || id != "claude-x" {
		t.Fatalf("Build(claude) = %q,%v, want claude-x,nil", id, err)
	}
	if c, id, err := reg.Build("claude-sonnet"); err != nil || id != "claude-sonnet-4-5" || c == nil {
		t.Fatalf("Build(claude-sonnet) = %q,%v, want claude-sonnet-4-5,nil", id, err)
	}

	// The sibling carries the same base_url + key_env as the base (shared
	// credential, not re-entered) and inherited the pricing pointer.
	got, ok := reg.GetModel("claude-sonnet")
	if !ok {
		t.Fatal("expected sibling claude-sonnet present")
	}
	if got.BaseURL != base.BaseURL || got.KeyEnv != base.KeyEnv || got.Backend != base.Backend {
		t.Fatalf("sibling credentials = %+v, want shared from base %+v", got, base)
	}
	if got.KeyEnv != "ANTHROPIC_API_KEY" {
		t.Fatalf("sibling key_env = %q, want ANTHROPIC_API_KEY", got.KeyEnv)
	}
	if got.PriceInput == nil || *got.PriceInput != price {
		t.Fatalf("sibling pricing = %v, want shared %v", got.PriceInput, price)
	}

	// Usage/cost attribution remains per logical name: the sibling is its own
	// distinct name with its own pricing entry.
	if p := reg.PricingFor("claude-sonnet"); !p.Configured || p.Input != price {
		t.Fatalf("sibling pricing resolve = %+v, want input %v", p, price)
	}
}

func TestUpsertModelValidation(t *testing.T) {
	reg := baseRegistry()
	if err := reg.UpsertModel("", Model{Backend: "openai", Model: "m"}, false); err == nil {
		t.Fatal("expected error for empty name")
	}
	if err := reg.UpsertModel("x", Model{Backend: "openai", Model: ""}, false); err == nil {
		t.Fatal("expected error for empty model id")
	}
	if err := reg.UpsertModel("x", Model{Backend: "nope", Model: "m"}, false); err == nil {
		t.Fatal("expected error for unsupported backend")
	}
	if reg.Has("x") {
		t.Fatal("invalid model must not be admitted")
	}
}

func TestRemoveModel(t *testing.T) {
	reg := baseRegistry()

	// A role-referenced model cannot be removed; the error names the role and
	// the model survives.
	err := reg.RemoveModel("claude", false)
	if err == nil {
		t.Fatal("expected error removing role-referenced model")
	}
	if !strings.Contains(err.Error(), RoleCoordinator) {
		t.Fatalf("error %q should mention referencing role", err)
	}
	if !reg.Has("claude") {
		t.Fatal("claude must still be present after rejected removal")
	}

	// An unreferenced model removes cleanly.
	if err := reg.UpsertModel("gpt", Model{Backend: "openai", Model: "gpt-4o"}, false); err != nil {
		t.Fatalf("UpsertModel(gpt): %v", err)
	}
	if err := reg.RemoveModel("gpt", false); err != nil {
		t.Fatalf("RemoveModel(gpt): %v", err)
	}
	if reg.Has("gpt") {
		t.Fatal("gpt should be gone after removal")
	}

	// Removing a missing model errors.
	if err := reg.RemoveModel("nope", false); err == nil {
		t.Fatal("expected error removing unknown model")
	}
}

func TestUpsertRemovePersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ycc.toml")
	reg := baseRegistry()
	reg.SetPath(path)

	// Persisted upsert: file written and reloads with the new model.
	if err := reg.UpsertModel("gpt", Model{Backend: "openai", BaseURL: "https://oai", Model: "gpt-4o", KeyEnv: "OPENAI_API_KEY"}, true); err != nil {
		t.Fatalf("UpsertModel persist: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load after persist: %v", err)
	}
	m, ok := loaded.Models["gpt"]
	if !ok || m.Model != "gpt-4o" || m.KeyEnv != "OPENAI_API_KEY" {
		t.Fatalf("reloaded gpt = %+v,%v", m, ok)
	}

	// Persisted remove: model gone after reload.
	if err := reg.RemoveModel("gpt", true); err != nil {
		t.Fatalf("RemoveModel persist: %v", err)
	}
	loaded2, err := Load(path)
	if err != nil {
		t.Fatalf("Load after remove: %v", err)
	}
	if _, ok := loaded2.Models["gpt"]; ok {
		t.Fatal("gpt should be gone from persisted config after removal")
	}
}

func TestPersistWithoutPathErrors(t *testing.T) {
	reg := baseRegistry() // no SetPath
	if err := reg.UpsertModel("gpt", Model{Backend: "openai", Model: "gpt-4o"}, true); err == nil {
		t.Fatal("expected error persisting without a config path")
	}
	if reg.Has("gpt") {
		t.Fatal("failed persist must revert the live change")
	}
}

func TestPersistFalseDoesNotWriteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ycc.toml")
	reg := baseRegistry()
	reg.SetPath(path)
	if err := reg.UpsertModel("gpt", Model{Backend: "openai", Model: "gpt-4o"}, false); err != nil {
		t.Fatalf("UpsertModel: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("persist=false must not write the config file (err=%v)", err)
	}
}

// --- review tiers (spec §13.1) ---

func TestReviewTierBuiltins(t *testing.T) {
	reg := baseRegistry()
	// "" selects the default (single-opus), not a fallback.
	def := reg.ReviewTier("")
	if def.Name != "single-opus" || def.Fallback {
		t.Fatalf("empty request = %+v, want single-opus no fallback", def)
	}
	if def.SelfReview || len(def.Models) != 1 || def.Models[0] != "claude" {
		t.Fatalf("single-opus models = %+v, want [claude] agents", def)
	}
	// simple built-in is coordinator self-review.
	simple := reg.ReviewTier("simple")
	if simple.Name != "simple" || !simple.SelfReview {
		t.Fatalf("simple = %+v, want self-review", simple)
	}
	// high-powered built-in exists.
	hp := reg.ReviewTier("high-powered")
	if hp.Name != "high-powered" || hp.SelfReview {
		t.Fatalf("high-powered = %+v, want agents tier", hp)
	}
	// Unknown tier degrades to the default with Fallback=true.
	unk := reg.ReviewTier("nope")
	if unk.Name != "single-opus" || !unk.Fallback {
		t.Fatalf("unknown request = %+v, want single-opus fallback", unk)
	}
}

func TestReviewTierConfiguredOverrides(t *testing.T) {
	reg := NewRegistry(&Config{
		Models: map[string]Model{
			"claude": {Backend: "anthropic", Model: "claude-x"},
			"gpt":    {Backend: "openai", Model: "gpt-x"},
		},
		Roles: Roles{Coordinator: "claude", Implementer: "claude", Reviewers: []string{"claude"}},
		Reviews: Reviews{
			Tiers: map[string]ReviewTier{
				"simple":       {Strategy: "coordinator"},
				"high-powered": {Strategy: "agents", Models: []string{"claude", "gpt"}},
			},
		},
	})
	simple := reg.ReviewTier("simple")
	if !simple.SelfReview {
		t.Fatalf("configured simple should be self-review: %+v", simple)
	}
	hp := reg.ReviewTier("high-powered")
	if hp.SelfReview || len(hp.Models) != 2 || hp.Models[0] != "claude" || hp.Models[1] != "gpt" {
		t.Fatalf("overridden high-powered = %+v, want [claude gpt] agents", hp)
	}
}

func TestReviewTierDefaultOverride(t *testing.T) {
	reg := NewRegistry(&Config{
		Models: map[string]Model{"claude": {Backend: "anthropic", Model: "claude-x"}},
		Roles:  Roles{Coordinator: "claude", Implementer: "claude", Reviewers: []string{"claude"}},
		Reviews: Reviews{
			Default: "simple",
			Tiers:   map[string]ReviewTier{"simple": {Strategy: "coordinator"}},
		},
	})
	def := reg.ReviewTier("")
	if def.Name != "simple" || !def.SelfReview || def.Fallback {
		t.Fatalf("default override = %+v, want simple self-review", def)
	}
}

func TestReviewTierSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ycc.toml")
	c := &Config{
		Models: map[string]Model{
			"claude": {Backend: "anthropic", Model: "claude-x"},
			"gpt":    {Backend: "openai", Model: "gpt-x"},
		},
		Roles: Roles{Coordinator: "claude", Implementer: "claude", Reviewers: []string{"claude"}},
		Reviews: Reviews{
			Default: "single-opus",
			Tiers: map[string]ReviewTier{
				"high-powered": {Strategy: "agents", Models: []string{"claude", "gpt"}},
				"simple":       {Strategy: "coordinator"},
			},
		},
	}
	if err := Save(path, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Reviews.Default != "single-opus" {
		t.Fatalf("round-trip default = %q, want single-opus", loaded.Reviews.Default)
	}
	if hp := loaded.Reviews.Tiers["high-powered"]; hp.Strategy != "agents" || !reflect.DeepEqual(hp.Models, []string{"claude", "gpt"}) {
		t.Fatalf("round-trip high-powered = %+v", hp)
	}
	if sp := loaded.Reviews.Tiers["simple"]; sp.Strategy != "coordinator" {
		t.Fatalf("round-trip simple = %+v, want coordinator strategy", sp)
	}
}

func TestReviewTierValidation(t *testing.T) {
	base := func() *Config {
		return &Config{
			Models: map[string]Model{"claude": {Backend: "anthropic", Model: "claude-x"}},
			Roles:  Roles{Coordinator: "claude", Implementer: "claude", Reviewers: []string{"claude"}},
		}
	}
	// Unknown strategy.
	c := base()
	c.Reviews.Tiers = map[string]ReviewTier{"x": {Strategy: "bogus"}}
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "unknown strategy") {
		t.Fatalf("expected unknown strategy error, got %v", err)
	}
	// Agents tier referencing an unknown model.
	c = base()
	c.Reviews.Tiers = map[string]ReviewTier{"x": {Strategy: "agents", Models: []string{"ghost"}}}
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("expected unknown model error, got %v", err)
	}
	// Default naming no tier.
	c = base()
	c.Reviews.Default = "ghost-tier"
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "unknown tier") {
		t.Fatalf("expected unknown tier error, got %v", err)
	}
	// A self-review tier with stray models is allowed (models ignored).
	c = base()
	c.Reviews.Tiers = map[string]ReviewTier{"x": {Strategy: "coordinator", Models: []string{"ghost"}}}
	if err := c.validate(); err != nil {
		t.Fatalf("self-review tier should validate, got %v", err)
	}
}

func TestResolveKeyPrecedence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)

	if err := secrets.Set("ANTHROPIC_API_KEY", "stored-token"); err != nil {
		t.Fatalf("secrets.Set: %v", err)
	}
	m := Model{Backend: "anthropic", KeyEnv: "ANTHROPIC_API_KEY"}

	// Env var unset: falls back to the stored token.
	t.Setenv("ANTHROPIC_API_KEY", "")
	if got := resolveKey(m); got != "stored-token" {
		t.Fatalf("resolveKey (env unset) = %q, want stored-token", got)
	}

	// Env var set: explicit env wins over the stored token.
	t.Setenv("ANTHROPIC_API_KEY", "env-token")
	if got := resolveKey(m); got != "env-token" {
		t.Fatalf("resolveKey (env set) = %q, want env-token", got)
	}

	// No key_env: empty.
	if got := resolveKey(Model{Backend: "ollama"}); got != "" {
		t.Fatalf("resolveKey (no key_env) = %q, want empty", got)
	}
}
