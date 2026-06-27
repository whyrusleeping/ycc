package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
