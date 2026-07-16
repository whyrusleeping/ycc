package config

import (
	"path/filepath"
	"testing"

	"github.com/whyrusleeping/ycc/internal/codex"
)

func TestEnsureSubscriptionModelFreshConfig(t *testing.T) {
	// Fresh (empty) config: the model is added under the default name and the
	// empty roles are pointed at it so the result validates and Saves.
	cfg := &Config{MaxTokens: DefaultMaxTokens}
	name, added, err := EnsureSubscriptionModel(cfg, "anthropic")
	if err != nil {
		t.Fatalf("EnsureSubscriptionModel: %v", err)
	}
	if !added || name != "claude" {
		t.Fatalf("got (%q, %v), want (\"claude\", true)", name, added)
	}
	m := cfg.Models["claude"]
	if m.Backend != "anthropic" || m.Auth != "oauth" || m.Model == "" {
		t.Fatalf("unexpected model record: %+v", m)
	}
	if cfg.Roles.Coordinator != "claude" || cfg.Roles.Implementer != "claude" ||
		len(cfg.Roles.Reviewers) != 1 || cfg.Roles.Reviewers[0] != "claude" {
		t.Fatalf("roles not defaulted to new model: %+v", cfg.Roles)
	}
	// Round-trips through Save/Load (i.e. passes validation).
	path := filepath.Join(t.TempDir(), "ycc.toml")
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestEnsureSubscriptionModelOpenAI(t *testing.T) {
	cfg := &Config{}
	name, added, err := EnsureSubscriptionModel(cfg, "openai")
	if err != nil || !added || name != "chatgpt" {
		t.Fatalf("got (%q, %v, %v), want (\"chatgpt\", true, nil)", name, added, err)
	}
	m := cfg.Models["chatgpt"]
	if m.Backend != "openai" || m.Auth != "oauth" || m.Model != codex.Models[0] {
		t.Fatalf("unexpected model record: %+v", m)
	}
	// Subscription tokens are not valid on the platform API; an empty
	// base_url resolves to the codex backend at Build time.
	if m.BaseURL != "" {
		t.Fatalf("BaseURL = %q, want empty (codex default)", m.BaseURL)
	}
}

func TestEnsureSubscriptionModelExistingConfig(t *testing.T) {
	// Existing config with an api-key model named "claude": a new entry is
	// added under a suffixed name and the existing roles are left alone.
	cfg := &Config{
		Models: map[string]Model{
			"claude": {Backend: "anthropic", Model: "claude-opus-4-8", KeyEnv: "ANTHROPIC_API_KEY"},
		},
		Roles: Roles{Coordinator: "claude", Implementer: "claude", Reviewers: []string{"claude"}},
	}
	name, added, err := EnsureSubscriptionModel(cfg, "anthropic")
	if err != nil {
		t.Fatalf("EnsureSubscriptionModel: %v", err)
	}
	if !added || name != "claude-oauth" {
		t.Fatalf("got (%q, %v), want (\"claude-oauth\", true)", name, added)
	}
	if cfg.Models["claude"].Auth != "" {
		t.Fatal("existing api-key model must not be mutated")
	}
	if cfg.Roles.Coordinator != "claude" {
		t.Fatalf("roles must not be touched, got %+v", cfg.Roles)
	}
}

func TestEnsureSubscriptionModelAlreadyConfigured(t *testing.T) {
	cfg := &Config{
		Models: map[string]Model{
			"sub": {Backend: "anthropic", Model: "claude-opus-4-8", Auth: "oauth"},
		},
		Roles: Roles{Coordinator: "sub", Implementer: "sub", Reviewers: []string{"sub"}},
	}
	name, added, err := EnsureSubscriptionModel(cfg, "anthropic")
	if err != nil {
		t.Fatalf("EnsureSubscriptionModel: %v", err)
	}
	if added || name != "sub" {
		t.Fatalf("got (%q, %v), want (\"sub\", false)", name, added)
	}
	// A different backend's oauth model does not satisfy this backend.
	name, added, err = EnsureSubscriptionModel(cfg, "openai")
	if err != nil || !added || name != "chatgpt" {
		t.Fatalf("got (%q, %v, %v), want (\"chatgpt\", true, nil)", name, added, err)
	}
}

func TestEnsureSubscriptionModelUnsupportedBackend(t *testing.T) {
	if _, _, err := EnsureSubscriptionModel(&Config{}, "ollama"); err == nil {
		t.Fatal("expected error for unsupported backend")
	}
}
