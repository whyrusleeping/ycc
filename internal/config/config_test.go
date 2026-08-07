package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/secrets"
)

// Compile-time guard: the gollama client must satisfy engine.StreamTurner so the
// engine loop's TurnStream seam engages with zero adapter code (task 0120). If a
// future gollama release changes the TurnStream signature, this breaks the build
// loudly instead of silently falling back to non-streaming turns.
var _ engine.StreamTurner = (*gollama.Client)(nil)

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

[integration]
base = "main"
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
	if got := NewRegistry(cfg).IntegrationBase(); got != "main" {
		t.Fatalf("registry IntegrationBase = %q, want main", got)
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

func TestSetRolesPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ycc.toml")
	reg := baseRegistry()
	reg.SetPath(path)
	// Add a second model so the role can actually change to a distinct target.
	if err := reg.UpsertModel("gpt", Model{Backend: "openai", BaseURL: "https://oai", Model: "gpt-4o", KeyEnv: "OPENAI_API_KEY"}, false); err != nil {
		t.Fatalf("UpsertModel(gpt): %v", err)
	}

	if err := reg.SetRoles("gpt", "gpt", []string{"gpt", "claude"}); err != nil {
		t.Fatalf("SetRoles: %v", err)
	}
	// Live view reflects it immediately.
	if reg.CoordinatorName() != "gpt" || reg.ImplementerName() != "gpt" {
		t.Fatalf("live roles not updated: coord=%q impl=%q", reg.CoordinatorName(), reg.ImplementerName())
	}
	// And it survives a reload from disk.
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load after SetRoles: %v", err)
	}
	if loaded.Roles.Coordinator != "gpt" || loaded.Roles.Implementer != "gpt" {
		t.Fatalf("persisted roles = %+v", loaded.Roles)
	}
	if len(loaded.Roles.Reviewers) != 2 || loaded.Roles.Reviewers[0] != "gpt" || loaded.Roles.Reviewers[1] != "claude" {
		t.Fatalf("persisted reviewers = %v", loaded.Roles.Reviewers)
	}

	// Empty args leave a role unchanged; an unknown model is rejected and does not
	// touch the persisted config.
	if err := reg.SetRoles("", "claude", nil); err != nil {
		t.Fatalf("SetRoles partial: %v", err)
	}
	if reg.CoordinatorName() != "gpt" || reg.ImplementerName() != "claude" {
		t.Fatalf("partial SetRoles: coord=%q impl=%q", reg.CoordinatorName(), reg.ImplementerName())
	}
	if err := reg.SetRoles("nope", "", nil); err == nil {
		t.Fatal("expected error setting role to unknown model")
	}
	if reg.CoordinatorName() != "gpt" {
		t.Fatal("failed SetRoles must not change the live role")
	}
}

func TestPersistWithoutPathIsInMemory(t *testing.T) {
	// With no config path, a persisted edit still applies in-memory instead of
	// failing — a runtime change should never be rejected just because there is
	// nowhere on disk to write it back to.
	reg := baseRegistry() // no SetPath
	if err := reg.UpsertModel("gpt", Model{Backend: "openai", Model: "gpt-4o"}, true); err != nil {
		t.Fatalf("UpsertModel without path should succeed in-memory: %v", err)
	}
	if !reg.Has("gpt") {
		t.Fatal("in-memory change should be applied even without a config path")
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
	if def.SelfReview || len(def.Reviewers) != 1 || def.Reviewers[0].Model != "claude" {
		t.Fatalf("single-opus reviewers = %+v, want [claude] agents", def)
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
	if hp.SelfReview || len(hp.Reviewers) != 2 || hp.Reviewers[0].Model != "claude" || hp.Reviewers[1].Model != "gpt" {
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

// A tier may task distinct reviewers — different models, or the same model twice
// — each with its own focus prompt and label (spec §13.1).
func TestReviewTierFocusedReviewers(t *testing.T) {
	reg := NewRegistry(&Config{
		Models: map[string]Model{
			"claude": {Backend: "anthropic", Model: "claude-x"},
			"gpt":    {Backend: "openai", Model: "gpt-x"},
		},
		Roles: Roles{Coordinator: "claude", Implementer: "claude", Reviewers: []string{"claude"}},
		Reviews: Reviews{
			Default: "deep",
			Tiers: map[string]ReviewTier{
				"deep": {
					Description: "risky changes",
					Prompt:      "Be concrete.",
					Reviewers: []Reviewer{
						{Name: "readability", Model: "claude", Prompt: "Focus on conciseness.", Thinking: "low"},
						{Name: "performance", Model: "gpt", Prompt: "Focus on performance."},
						{Model: "claude"},
						{Model: "claude"}, // duplicate label gets disambiguated
					},
				},
			},
		},
	})
	td := reg.ReviewTier("")
	if td.Name != "deep" || td.SelfReview || td.Description != "risky changes" {
		t.Fatalf("resolved deep tier = %+v", td)
	}
	want := []ResolvedReviewer{
		{Label: "readability", Model: "claude", Prompt: "Be concrete.\n\nFocus on conciseness.", Thinking: "low"},
		{Label: "performance", Model: "gpt", Prompt: "Be concrete.\n\nFocus on performance."},
		{Label: "claude", Model: "claude", Prompt: "Be concrete."},
		{Label: "claude#2", Model: "claude", Prompt: "Be concrete."},
	}
	if !reflect.DeepEqual(td.Reviewers, want) {
		t.Fatalf("resolved reviewers = %+v, want %+v", td.Reviewers, want)
	}
	// ReviewTiers lists every effective tier, default first.
	tiers := reg.ReviewTiers()
	if len(tiers) == 0 || tiers[0].Name != "deep" || !tiers[0].Default {
		t.Fatalf("ReviewTiers should list the default first: %+v", tiers)
	}
	byName := map[string]ReviewTierInfo{}
	for _, ti := range tiers {
		byName[ti.Name] = ti
	}
	if s, ok := byName["simple"]; !ok || !s.SelfReview || s.Description == "" {
		t.Fatalf("built-in simple tier missing/incomplete: %+v", byName)
	}
}

// The long-form reviewers list round-trips through Save/Load.
func TestReviewTierReviewersRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ycc.toml")
	c := &Config{
		Models: map[string]Model{
			"claude": {Backend: "anthropic", Model: "claude-x"},
			"gpt":    {Backend: "openai", Model: "gpt-x"},
		},
		Roles: Roles{Coordinator: "claude", Implementer: "claude", Reviewers: []string{"claude"}},
		Reviews: Reviews{
			Default: "deep",
			Tiers: map[string]ReviewTier{
				"deep": {
					Description: "risky changes",
					Prompt:      "Be concrete.",
					Reviewers: []Reviewer{
						{Name: "readability", Model: "claude", Prompt: "Focus on conciseness."},
						{Name: "performance", Model: "gpt", Prompt: "Focus on performance.", Thinking: "max"},
					},
				},
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
	if !reflect.DeepEqual(loaded.Reviews.Tiers["deep"], c.Reviews.Tiers["deep"]) {
		t.Fatalf("round-trip deep tier = %+v, want %+v", loaded.Reviews.Tiers["deep"], c.Reviews.Tiers["deep"])
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
	// A reviewer entry referencing an unknown model.
	c = base()
	c.Reviews.Tiers = map[string]ReviewTier{"x": {Reviewers: []Reviewer{{Model: "ghost"}}}}
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("expected unknown model error, got %v", err)
	}
	// A reviewer entry with no model at all.
	c = base()
	c.Reviews.Tiers = map[string]ReviewTier{"x": {Reviewers: []Reviewer{{Prompt: "focus"}}}}
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "no model") {
		t.Fatalf("expected missing-model error, got %v", err)
	}
	// A reviewer with an invalid thinking level.
	c = base()
	c.Reviews.Tiers = map[string]ReviewTier{"x": {Reviewers: []Reviewer{{Model: "claude", Thinking: "turbo"}}}}
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "unknown thinking level") {
		t.Fatalf("expected thinking-level error, got %v", err)
	}
	// models and reviewers are mutually exclusive.
	c = base()
	c.Reviews.Tiers = map[string]ReviewTier{"x": {Models: []string{"claude"}, Reviewers: []Reviewer{{Model: "claude"}}}}
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("expected models/reviewers conflict error, got %v", err)
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

// DefaultMaxTokens is the shared per-turn output cap default; it was raised so
// extended-thinking budgets aren't exhausted mid-turn. Pin the value so the
// config default, daemon options, and CLI flag default stay in sync.
func TestDefaultMaxTokens(t *testing.T) {
	if DefaultMaxTokens != 32000 {
		t.Fatalf("DefaultMaxTokens = %d, want 32000", DefaultMaxTokens)
	}
}

// Budget caps round-trip through Save/Load, negatives are rejected, and the
// Registry accessor returns the configured caps (task 0137, spec §20.6).
func TestBudgetRoundTripAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ycc.toml")
	orig := &Config{
		Models: map[string]Model{"claude": {Backend: "anthropic", BaseURL: "u", Model: "m", KeyEnv: "K"}},
		Roles:  Roles{Coordinator: "claude", Implementer: "claude", Reviewers: []string{"claude"}},
		Budget: Budget{SessionCost: 5.0, SessionTokens: 2_000_000, LoopCost: 20.0, LoopTokens: 8_000_000},
	}
	if err := Save(path, orig); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got.Budget, orig.Budget) {
		t.Fatalf("budget round-trip mismatch: got=%+v want=%+v", got.Budget, orig.Budget)
	}

	// Registry accessor reflects the configured caps.
	reg := NewRegistry(got)
	if b := reg.Budget(); b != orig.Budget {
		t.Fatalf("Registry.Budget() = %+v, want %+v", b, orig.Budget)
	}

	// Negative values are rejected by validate (via Save).
	bad := &Config{
		Models: orig.Models, Roles: orig.Roles,
		Budget: Budget{SessionCost: -1},
	}
	if err := Save(filepath.Join(t.TempDir(), "bad.toml"), bad); err == nil {
		t.Fatal("Save with negative budget succeeded, want error")
	}
}

// An absent [budget] block means every cap is 0 (unlimited).
func TestBudgetDefaultUnlimited(t *testing.T) {
	reg := NewRegistry(&Config{
		Models: map[string]Model{"c": {Backend: "ollama", Model: "m"}},
		Roles:  Roles{Coordinator: "c", Implementer: "c", Reviewers: []string{"c"}},
	})
	if b := reg.Budget(); b != (Budget{}) {
		t.Fatalf("default Budget = %+v, want zero (unlimited)", b)
	}
}

func TestNotifyRoundTripAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ycc.toml")
	orig := &Config{
		Models: map[string]Model{"claude": {Backend: "anthropic", BaseURL: "u", Model: "m", KeyEnv: "K"}},
		Roles:  Roles{Coordinator: "claude", Implementer: "claude", Reviewers: []string{"claude"}},
		Notify: Notify{URL: "https://ntfy.sh/mytopic", Auth: "Bearer tk_x", Events: []string{"question", "digest"}},
	}
	if err := Save(path, orig); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got.Notify, orig.Notify) {
		t.Fatalf("notify round-trip mismatch: got=%+v want=%+v", got.Notify, orig.Notify)
	}

	// Registry accessor reflects the configured notifier.
	reg := NewRegistry(got)
	if n := reg.Notify(); !reflect.DeepEqual(n, orig.Notify) {
		t.Fatalf("Registry.Notify() = %+v, want %+v", n, orig.Notify)
	}

	// An unknown event kind is rejected by validate (via Save).
	bad := &Config{
		Models: orig.Models, Roles: orig.Roles,
		Notify: Notify{URL: "https://ntfy.sh/x", Events: []string{"bogus"}},
	}
	if err := Save(filepath.Join(t.TempDir(), "bad.toml"), bad); err == nil {
		t.Fatal("Save with unknown notify event kind succeeded, want error")
	}
}

// An absent [notify] block means notifications are disabled (empty URL).
func TestNotifyDefaultDisabled(t *testing.T) {
	reg := NewRegistry(&Config{
		Models: map[string]Model{"c": {Backend: "ollama", Model: "m"}},
		Roles:  Roles{Coordinator: "c", Implementer: "c", Reviewers: []string{"c"}},
	})
	if n := reg.Notify(); n.URL != "" {
		t.Fatalf("default Notify.URL = %q, want empty (disabled)", n.URL)
	}
}

// A [retry] block round-trips through Save/Load and RetryPolicy overlays each
// configured field onto the engine default (task 0133, spec §7.2).
func TestRetryRoundTripAndPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ycc.toml")
	orig := &Config{
		Models: map[string]Model{"claude": {Backend: "anthropic", BaseURL: "u", Model: "m", KeyEnv: "K"}},
		Roles:  Roles{Coordinator: "claude", Implementer: "claude", Reviewers: []string{"claude"}},
		Retry:  Retry{MaxAttempts: 5, BaseDelayMS: 250, MaxDelayMS: 10000},
	}
	if err := Save(path, orig); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got.Retry, orig.Retry) {
		t.Fatalf("retry round-trip mismatch: got=%+v want=%+v", got.Retry, orig.Retry)
	}

	reg := NewRegistry(got)
	want := engine.RetryPolicy{MaxAttempts: 5, BaseDelay: 250 * time.Millisecond, MaxDelay: 10 * time.Second}
	if p := reg.RetryPolicy(); p != want {
		t.Fatalf("Registry.RetryPolicy() = %+v, want %+v", p, want)
	}
}

// [retry] parses from TOML and a partial block overlays only the set fields,
// leaving the rest at the engine default.
func TestRetryParsePartialOverlay(t *testing.T) {
	const src = `
[models.c]
backend = "ollama"
model = "m"

[roles]
coordinator = "c"
implementer = "c"
reviewers = ["c"]

[retry]
max_attempts = 7
`
	path := filepath.Join(t.TempDir(), "ycc.toml")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Retry.MaxAttempts != 7 {
		t.Fatalf("Retry.MaxAttempts = %d, want 7", c.Retry.MaxAttempts)
	}
	def := engine.DefaultRetryPolicy()
	want := engine.RetryPolicy{MaxAttempts: 7, BaseDelay: def.BaseDelay, MaxDelay: def.MaxDelay}
	if p := NewRegistry(c).RetryPolicy(); p != want {
		t.Fatalf("RetryPolicy() = %+v, want %+v (only max_attempts overlaid)", p, want)
	}
}

// An absent [retry] block yields exactly the engine default policy.
func TestRetryDefault(t *testing.T) {
	reg := NewRegistry(&Config{
		Models: map[string]Model{"c": {Backend: "ollama", Model: "m"}},
		Roles:  Roles{Coordinator: "c", Implementer: "c", Reviewers: []string{"c"}},
	})
	if p := reg.RetryPolicy(); p != engine.DefaultRetryPolicy() {
		t.Fatalf("default RetryPolicy() = %+v, want %+v", p, engine.DefaultRetryPolicy())
	}
}

// max_attempts = 1 disables retry entirely (policy has MaxAttempts 1, so the
// loop's "zero => default" fallback never kicks in).
func TestRetryMaxAttemptsOneDisables(t *testing.T) {
	reg := NewRegistry(&Config{
		Models: map[string]Model{"c": {Backend: "ollama", Model: "m"}},
		Roles:  Roles{Coordinator: "c", Implementer: "c", Reviewers: []string{"c"}},
		Retry:  Retry{MaxAttempts: 1},
	})
	if p := reg.RetryPolicy(); p.MaxAttempts != 1 {
		t.Fatalf("RetryPolicy().MaxAttempts = %d, want 1 (retry disabled)", p.MaxAttempts)
	}
}

// validate rejects negative retry values and max_delay_ms < base_delay_ms.
func TestRetryValidation(t *testing.T) {
	base := func() *Config {
		return &Config{
			Models: map[string]Model{"c": {Backend: "ollama", Model: "m"}},
			Roles:  Roles{Coordinator: "c", Implementer: "c", Reviewers: []string{"c"}},
		}
	}
	neg := base()
	neg.Retry = Retry{MaxAttempts: -1}
	if err := neg.validate(); err == nil {
		t.Fatal("validate with negative max_attempts succeeded, want error")
	}
	negDelay := base()
	negDelay.Retry = Retry{BaseDelayMS: -5}
	if err := negDelay.validate(); err == nil {
		t.Fatal("validate with negative base_delay_ms succeeded, want error")
	}
	inverted := base()
	inverted.Retry = Retry{BaseDelayMS: 1000, MaxDelayMS: 500}
	if err := inverted.validate(); err == nil {
		t.Fatal("validate with max_delay_ms < base_delay_ms succeeded, want error")
	}
	ok := base()
	ok.Retry = Retry{BaseDelayMS: 500, MaxDelayMS: 500}
	if err := ok.validate(); err != nil {
		t.Fatalf("validate with equal delays failed: %v", err)
	}
}

func TestWorkImplementationDefaultAndSet(t *testing.T) {
	// Unset defaults to "delegate".
	if got := (Work{}).ResolvedImplementation(); got != ImplementationDelegate {
		t.Fatalf("empty Work.ResolvedImplementation = %q, want %q", got, ImplementationDelegate)
	}
	reg := baseRegistry()
	if got := reg.WorkImplementation(); got != ImplementationDelegate {
		t.Fatalf("default WorkImplementation = %q, want %q", got, ImplementationDelegate)
	}

	path := filepath.Join(t.TempDir(), "ycc.toml")
	reg.SetPath(path)
	if err := reg.SetWorkImplementation(ImplementationDirect); err != nil {
		t.Fatalf("SetWorkImplementation(direct): %v", err)
	}
	if got := reg.WorkImplementation(); got != ImplementationDirect {
		t.Fatalf("live WorkImplementation = %q, want %q", got, ImplementationDirect)
	}
	// Survives a reload from disk.
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load after SetWorkImplementation: %v", err)
	}
	if loaded.Work.ResolvedImplementation() != ImplementationDirect {
		t.Fatalf("persisted work implementation = %q", loaded.Work.Implementation)
	}
	// A bogus value is rejected and leaves the live config unchanged.
	if err := reg.SetWorkImplementation("bogus"); err == nil {
		t.Fatal("SetWorkImplementation(bogus) succeeded, want error")
	}
	if got := reg.WorkImplementation(); got != ImplementationDirect {
		t.Fatalf("rejected set changed live value to %q", got)
	}
}

func TestWorkImplementationValidation(t *testing.T) {
	base := func() *Config {
		return &Config{
			Models: map[string]Model{"c": {Backend: "ollama", Model: "m"}},
			Roles:  Roles{Coordinator: "c", Implementer: "c", Reviewers: []string{"c"}},
		}
	}
	bad := base()
	bad.Work = Work{Implementation: "sometimes"}
	if err := bad.validate(); err == nil {
		t.Fatal("validate with unknown work.implementation succeeded, want error")
	}
	for _, v := range []string{"", ImplementationDelegate, ImplementationDirect} {
		c := base()
		c.Work = Work{Implementation: v}
		if err := c.validate(); err != nil {
			t.Fatalf("validate work.implementation=%q failed: %v", v, err)
		}
	}
}

// The [reviews] TOML shape documented in spec §13.1 parses and resolves as
// written: named tiers, a shorthand models tier, and a long-form tier whose
// reviewers each carry their own model, label, focus prompt, and thinking level.
func TestReviewTierSpecTOMLShape(t *testing.T) {
	toml := `
[models.claude]
backend = "anthropic"
model = "claude-x"
[models.gpt]
backend = "openai"
model = "gpt-x"

[roles]
coordinator = "claude"
implementer = "claude"
reviewers = ["claude"]

[reviews]
default = "standard"

[reviews.tiers.standard]
models = ["claude"]

[reviews.tiers.deep]
description = "large, risky, or performance-sensitive changes"
prompt = "Cite file:line for every finding."

  [[reviews.tiers.deep.reviewers]]
  name   = "readability"
  model  = "claude"
  prompt = "Focus on conciseness."
  thinking = "high"

  [[reviews.tiers.deep.reviewers]]
  name   = "performance"
  model  = "gpt"
  prompt = "Focus on performance."

[reviews.tiers.simple]
strategy = "coordinator"
`
	c, err := loadSpecTOML(t, toml)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	reg := NewRegistry(c)
	td := reg.ReviewTier("deep")
	t.Logf("%+v", td)
	if len(td.Reviewers) != 2 || td.Reviewers[1].Model != "gpt" {
		t.Fatalf("bad: %+v", td)
	}
	if td.Reviewers[0].Prompt != "Cite file:line for every finding.\n\nFocus on conciseness." {
		t.Fatalf("prompt: %q", td.Reviewers[0].Prompt)
	}
	if reg.ReviewTier("").Name != "standard" {
		t.Fatalf("default: %+v", reg.ReviewTier(""))
	}
}

func TestWorktreeConfigParseRoundTripAndValidation(t *testing.T) {
	cfg := DefaultAnthropic("https://api", "claude", "KEY", 4096)
	cfg.Worktree = Worktree{
		Copy:                []string{".env", ".env.local"},
		Link:                []string{"node_modules", ".venv"},
		Setup:               []string{"go mod download"},
		Env:                 map[string]string{"FOO": "bar", "COUNT": "2"},
		SetupTimeoutSeconds: 42,
	}
	path := filepath.Join(t.TempDir(), "ycc.toml")
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got.Worktree, cfg.Worktree) {
		t.Fatalf("worktree round trip = %+v, want %+v", got.Worktree, cfg.Worktree)
	}

	cfg.Worktree.SetupTimeoutSeconds = -1
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "worktree.setup_timeout_seconds") {
		t.Fatalf("negative worktree timeout validation = %v", err)
	}
}

func TestLoadWorktreeLenient(t *testing.T) {
	dir := t.TempDir()
	partial := `
unknown_top_level = "ignored"
[worktree]
copy = [".env"]
link = ["node_modules"]
setup = ["make prepare"]
setup_timeout_seconds = 17
env = { FOO = "bar" }
`
	if err := os.WriteFile(filepath.Join(dir, "ycc.toml"), []byte(partial), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok := LoadWorktree(dir)
	if !ok {
		t.Fatal("LoadWorktree did not find partial [worktree] table")
	}
	want := Worktree{
		Copy: []string{".env"}, Link: []string{"node_modules"}, Setup: []string{"make prepare"},
		Env: map[string]string{"FOO": "bar"}, SetupTimeoutSeconds: 17,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadWorktree = %+v, want %+v", got, want)
	}

	if _, ok := LoadWorktree(t.TempDir()); ok {
		t.Fatal("missing ycc.toml reported a worktree config")
	}
	absent := t.TempDir()
	if err := os.WriteFile(filepath.Join(absent, "ycc.toml"), []byte("other = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadWorktree(absent); ok {
		t.Fatal("absent [worktree] table reported a config")
	}
	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, "ycc.toml"), []byte("[worktree]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadWorktree(empty); ok {
		t.Fatal("empty [worktree] table reported a config")
	}
}

func TestRegistryWorktreeConfigIsDeepCopy(t *testing.T) {
	original := Worktree{Copy: []string{".env"}, Env: map[string]string{"FOO": "bar"}}
	reg := NewRegistry(&Config{Worktree: original})
	got := reg.WorktreeConfig()
	got.Copy[0] = "changed"
	got.Env["FOO"] = "changed"
	again := reg.WorktreeConfig()
	if again.Copy[0] != ".env" || again.Env["FOO"] != "bar" {
		t.Fatalf("registry worktree config was aliased: %+v", again)
	}
}

func loadSpecTOML(t *testing.T, s string) (*Config, error) {
	t.Helper()
	p := t.TempDir() + "/ycc.toml"
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}
