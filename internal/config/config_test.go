package config

import (
	"os"
	"path/filepath"
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

[models.haiku]
backend = "anthropic"
base_url = "https://api.anthropic.com"
model = "claude-haiku-4-5"
key_env = "ANTHROPIC_API_KEY"

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
