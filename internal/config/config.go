// Package config loads the model/role configuration (spec §13) and builds the
// per-role gollama backends the engine uses. A logical model name (e.g. "claude",
// "gpt", "glm", "local") maps to a backend + base URL + model id + key env var;
// roles (coordinator, implementer, reviewers) reference those logical names, so
// review can fan out across genuinely different models/providers.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

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

	// Thinking / Effort / ThinkingDisplay control Anthropic extended/adaptive
	// reasoning per-model (spec §7, §13). They are honored by the anthropic
	// backend and ignored harmlessly by others. When unset, sensible
	// reasoning-on defaults apply (see ResolveThinking) — this is an agentic
	// coding harness, so reasoning is desired by default. Set thinking = "off"
	// (or "") to disable thinking for a model.
	Thinking        string `toml:"thinking"`         // "adaptive" | "off" | ""
	Effort          string `toml:"effort"`           // "low" | "medium" | "high" | "xhigh" | "max"
	ThinkingDisplay string `toml:"thinking_display"` // "summarized" | "omitted"
}

// Thinking carries the resolved per-model reasoning settings the engine plumbs
// into gollama.RequestOptions. An empty Thinking field means reasoning is off
// (Effort/ThinkingDisplay are then irrelevant to the request).
type Thinking struct {
	Thinking        string
	Effort          string
	ThinkingDisplay string
}

// Default reasoning settings applied when a model leaves them unset. This is an
// agentic coding harness, so reasoning-on is the desired default.
const (
	defaultThinking        = "adaptive"
	defaultEffort          = "high"
	defaultThinkingDisplay = "summarized"
)

// ResolveThinking fills in defaults for unset fields and normalizes the
// "off"/disabled cases. A model that does not opt out gets adaptive thinking at
// high effort with summarized display; thinking = "off" (or "none"/"disabled")
// turns reasoning off entirely (empty Thinking on the returned struct).
func (m Model) ResolveThinking() Thinking {
	think := m.Thinking
	if think == "" {
		think = defaultThinking
	}
	switch think {
	case "off", "none", "disabled", "false":
		return Thinking{} // reasoning disabled
	}
	effort := m.Effort
	if effort == "" {
		effort = defaultEffort
	}
	display := m.ThinkingDisplay
	if display == "" {
		display = defaultThinkingDisplay
	}
	return Thinking{Thinking: think, Effort: effort, ThinkingDisplay: display}
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
	// MaxTurns caps the number of tool-call turns in a single engine Run as a
	// runaway/cost backstop. 0 means "use the engine's high default" (see
	// engine.defaultMaxTurns). It sits beside max_tokens in the config.
	//
	// Note (task 0010): raising this lets a run accumulate more context, so a
	// very high value can trade a turn-limit abort for a context-window-limit
	// abort until context-window management (0010) lands.
	MaxTurns int `toml:"max_turns"`
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

// Save validates c and writes it to path as TOML that Load reads back to an equal
// *Config. Parent directories are created as needed. Keys are persisted as
// key_env references only (the Model struct has no inline-secret field), so no
// secret values are ever written. An invalid config is rejected before anything
// is written, so we never persist a config Load would reject.
func Save(path string, c *Config) error {
	if c == nil {
		return fmt.Errorf("config: cannot Save nil config")
	}
	if err := c.validate(); err != nil {
		return err
	}
	data, err := toml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create config dir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// DefaultAnthropic returns a single-backend config (one logical model "claude"
// used for every role) for the common case / daemon fallback when no config file
// is provided.
func DefaultAnthropic(baseURL, model, keyEnv string, maxTokens int) *Config {
	return &Config{
		Models: map[string]Model{
			"claude": {
				Backend: "anthropic", BaseURL: baseURL, Model: model, KeyEnv: keyEnv,
				// Carry the reasoning-on defaults explicitly so the no-config
				// single-backend path gets adaptive thinking + high effort too.
				Thinking: defaultThinking, Effort: defaultEffort, ThinkingDisplay: defaultThinkingDisplay,
			},
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

// MaxTurns returns the configured per-Run tool-call turn cap (0 if unset, in
// which case the engine applies its high default backstop).
func (r *Registry) MaxTurns() int { return r.cfg.MaxTurns }

// CoordinatorName / ImplementerName / ReviewerNames expose the role assignments.
func (r *Registry) CoordinatorName() string { return r.cfg.Roles.Coordinator }
func (r *Registry) ImplementerName() string { return r.cfg.Roles.Implementer }
func (r *Registry) ReviewerNames() []string { return r.cfg.Roles.Reviewers }

// ModelInfo describes a configured logical model for enumeration (ListModels).
type ModelInfo struct {
	Name    string
	Backend string
	Model   string
}

// ThinkingFor returns the resolved reasoning settings for a logical model name,
// applying defaults for unset fields. Unknown names return the package defaults
// so callers always get reasoning-on behaviour.
func (r *Registry) ThinkingFor(name string) Thinking {
	m, ok := r.cfg.Models[name]
	if !ok {
		return Model{}.ResolveThinking()
	}
	return m.ResolveThinking()
}

// Has reports whether a logical model name is configured.
func (r *Registry) Has(name string) bool {
	_, ok := r.cfg.Models[name]
	return ok
}

// Models returns the configured logical models sorted by name so the settings
// overlay can populate the per-role pickers (spec §13, §18.2).
func (r *Registry) Models() []ModelInfo {
	names := make([]string, 0, len(r.cfg.Models))
	for name := range r.cfg.Models {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]ModelInfo, 0, len(names))
	for _, name := range names {
		m := r.cfg.Models[name]
		out = append(out, ModelInfo{Name: name, Backend: m.Backend, Model: m.Model})
	}
	return out
}

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
		// Pin the native Anthropic transport explicitly rather than relying on
		// gollama's URL auto-detection (which only matches "anthropic.com").
		// This guarantees the Anthropic /messages path — and with it the
		// automatic cache_control: ephemeral breakpoints (system prompt, last
		// tool definition, recent messages) that drive prompt caching — is used
		// even when the model routes through a proxy/gateway on a custom domain.
		c.SetAnthropicMode(true)
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
