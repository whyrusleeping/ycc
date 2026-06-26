// Package config loads the model/role configuration (spec §13) and builds the
// per-role gollama backends the engine uses. A logical model name (e.g. "claude",
// "gpt", "glm", "local") maps to a backend + base URL + model id + key env var;
// roles (coordinator, implementer, reviewers) reference those logical names, so
// review can fan out across genuinely different models/providers.
package config

import (
	"fmt"
	"os"

	"github.com/pelletier/go-toml/v2"
	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/engine"
)

// Model describes one logical backend.
type Model struct {
	Backend string `toml:"backend"` // anthropic | openai | ollama
	BaseURL string `toml:"base_url"`
	Model   string `toml:"model"`
	KeyEnv  string `toml:"key_env"`
}

// Roles assigns logical model names to workflow roles.
type Roles struct {
	Coordinator string   `toml:"coordinator"`
	Implementer string   `toml:"implementer"`
	Reviewers   []string `toml:"reviewers"`
}

// Config is the whole ycc configuration.
type Config struct {
	Models    map[string]Model `toml:"models"`
	Roles     Roles            `toml:"roles"`
	MaxTokens int              `toml:"max_tokens"`
}

// Load reads and validates a TOML config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := toml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// DefaultAnthropic returns a single-backend config (one logical model "claude"
// used for every role) for the common case / daemon fallback when no config file
// is provided.
func DefaultAnthropic(baseURL, model, keyEnv string, maxTokens int) *Config {
	return &Config{
		Models: map[string]Model{
			"claude": {Backend: "anthropic", BaseURL: baseURL, Model: model, KeyEnv: keyEnv},
		},
		Roles:     Roles{Coordinator: "claude", Implementer: "claude", Reviewers: []string{"claude"}},
		MaxTokens: maxTokens,
	}
}

func (c *Config) validate() error {
	if c.Roles.Coordinator == "" || c.Roles.Implementer == "" {
		return fmt.Errorf("roles.coordinator and roles.implementer are required")
	}
	if len(c.Roles.Reviewers) == 0 {
		return fmt.Errorf("at least one roles.reviewers entry is required")
	}
	for _, name := range append([]string{c.Roles.Coordinator, c.Roles.Implementer}, c.Roles.Reviewers...) {
		if _, ok := c.Models[name]; !ok {
			return fmt.Errorf("role references unknown model %q", name)
		}
	}
	return nil
}

// Registry builds backends from a Config.
type Registry struct {
	cfg *Config
}

// NewRegistry returns a Registry over cfg.
func NewRegistry(cfg *Config) *Registry { return &Registry{cfg: cfg} }

// MaxTokens returns the configured per-turn token cap (0 if unset).
func (r *Registry) MaxTokens() int { return r.cfg.MaxTokens }

// CoordinatorName / ImplementerName / ReviewerNames expose the role assignments.
func (r *Registry) CoordinatorName() string  { return r.cfg.Roles.Coordinator }
func (r *Registry) ImplementerName() string  { return r.cfg.Roles.Implementer }
func (r *Registry) ReviewerNames() []string  { return r.cfg.Roles.Reviewers }

// Build constructs a fresh backend client and returns it with its model id. A new
// client per call avoids shared-state races across concurrent subagents.
func (r *Registry) Build(name string) (engine.Turner, string, error) {
	m, ok := r.cfg.Models[name]
	if !ok {
		return nil, "", fmt.Errorf("unknown model %q", name)
	}
	c := gollama.NewClient(m.BaseURL)
	key := ""
	if m.KeyEnv != "" {
		key = os.Getenv(m.KeyEnv)
	}
	switch m.Backend {
	case "anthropic":
		if key != "" {
			c.SetAPIKey(key)
		}
	case "openai", "openai-compatible", "glm":
		if key != "" {
			c.SetBearerToken(key)
		}
	case "ollama":
		// no auth
	default:
		return nil, "", fmt.Errorf("model %q: unsupported backend %q", name, m.Backend)
	}
	return c, m.Model, nil
}
