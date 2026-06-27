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
	"github.com/whyrusleeping/ycc/internal/event"
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

	// Optional per-model pricing in US dollars per million tokens (spec §20.4),
	// split by token class so cache reads/writes can be priced separately from
	// fresh input/output. Each is a pointer so an unset price is distinguishable
	// from an explicit 0.0 (and round-trips through config.Save). Pricing lives
	// in config — not code — so it can track vendor price changes without
	// touching the event log. When none are set the model is "unpriced" and cost
	// is reported as unknown rather than invented as 0.
	PriceInput      *float64 `toml:"price_input,omitempty"`
	PriceOutput     *float64 `toml:"price_output,omitempty"`
	PriceCacheRead  *float64 `toml:"price_cache_read,omitempty"`
	PriceCacheWrite *float64 `toml:"price_cache_write,omitempty"`
}

// Pricing holds the resolved per-token-class rates for a model in US dollars per
// million tokens (spec §20.4). Configured reports whether ANY rate was set; when
// false the model is unpriced and Cost returns priced=false so callers display
// cost as unknown ("—") rather than 0.
type Pricing struct {
	Input      float64 // $/Mtok fresh input
	Output     float64 // $/Mtok output
	CacheRead  float64 // $/Mtok cache read
	CacheWrite float64 // $/Mtok cache write
	Configured bool
}

// Pricing resolves the model's pricing fields into a Pricing value. Configured
// is true if at least one of the four rate fields is set; unset (nil) fields
// leave the corresponding rate at 0.
func (m Model) Pricing() Pricing {
	var p Pricing
	if m.PriceInput != nil {
		p.Input = *m.PriceInput
		p.Configured = true
	}
	if m.PriceOutput != nil {
		p.Output = *m.PriceOutput
		p.Configured = true
	}
	if m.PriceCacheRead != nil {
		p.CacheRead = *m.PriceCacheRead
		p.Configured = true
	}
	if m.PriceCacheWrite != nil {
		p.CacheWrite = *m.PriceCacheWrite
		p.Configured = true
	}
	return p
}

// Cost returns the dollar cost of a usage breakdown under this pricing, and
// whether it could be priced. cost = Σ(tokens_class × rate_class) / 1e6 (rates
// are $/Mtok). When pricing is not configured, priced is false and callers
// should display cost as unknown ("—") rather than 0 — the feature must never
// invent numbers (spec §20.4).
func (p Pricing) Cost(u event.Usage) (cost float64, priced bool) {
	if !p.Configured {
		return 0, false
	}
	cost = (float64(u.Input)*p.Input +
		float64(u.Output)*p.Output +
		float64(u.CacheRead)*p.CacheRead +
		float64(u.CacheWrite)*p.CacheWrite) / 1e6
	return cost, true
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

// RoleThinking carries an optional per-role reasoning override (spec §7.4, §13,
// §18.2). Each field is a single-knob level (off|low|medium|high|xhigh|max); an
// empty field means "unset" and falls back to the per-model config then package
// defaults. The reviewers level applies uniformly to the whole reviewer fan-out.
type RoleThinking struct {
	Coordinator string `toml:"coordinator,omitempty"`
	Implementer string `toml:"implementer,omitempty"`
	Reviewers   string `toml:"reviewers,omitempty"`
}

// Roles assigns logical model names to workflow roles.
type Roles struct {
	Coordinator string   `toml:"coordinator"`
	Implementer string   `toml:"implementer"`
	Reviewers   []string `toml:"reviewers"`
	// Thinking optionally overrides the reasoning level per role, layered above
	// the per-model config (spec §7.4). Unset roles fall back to per-model.
	Thinking RoleThinking `toml:"thinking,omitempty"`
}

// Role name constants for per-role lookups.
const (
	RoleCoordinator = "coordinator"
	RoleImplementer = "implementer"
	RoleReviewers   = "reviewers"
)

// validThinkingLevel reports whether s is an allowed single-knob thinking level.
func validThinkingLevel(s string) bool {
	switch s {
	case "off", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
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
	for role, lvl := range map[string]string{
		RoleCoordinator: c.Roles.Thinking.Coordinator,
		RoleImplementer: c.Roles.Thinking.Implementer,
		RoleReviewers:   c.Roles.Thinking.Reviewers,
	} {
		if lvl != "" && !validThinkingLevel(lvl) {
			return fmt.Errorf("roles.thinking.%s: unknown thinking level %q", role, lvl)
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

// RoleThinking returns the configured per-role thinking level for a role
// ("coordinator"|"implementer"|"reviewers"). ok is false when the role has no
// per-role override configured (so callers fall back to per-model config).
func (r *Registry) RoleThinking(role string) (string, bool) {
	var lvl string
	switch role {
	case RoleCoordinator:
		lvl = r.cfg.Roles.Thinking.Coordinator
	case RoleImplementer:
		lvl = r.cfg.Roles.Thinking.Implementer
	case RoleReviewers:
		lvl = r.cfg.Roles.Thinking.Reviewers
	}
	if lvl == "" {
		return "", false
	}
	return lvl, true
}

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

// PricingFor returns the resolved pricing for a logical model name (spec §20.4).
// An unknown name returns the zero (unconfigured) Pricing, so cost is reported
// as unknown rather than invented.
func (r *Registry) PricingFor(name string) Pricing {
	m, ok := r.cfg.Models[name]
	if !ok {
		return Pricing{}
	}
	return m.Pricing()
}

// Has reports whether a logical model name is configured.
func (r *Registry) Has(name string) bool {
	_, ok := r.cfg.Models[name]
	return ok
}

// BackendFor returns the logical backend family ("anthropic", "openai", ...) for
// a configured model name, or "" if unknown. Used to label per-turn usage events
// (spec §20.1) with the backend that produced them.
func (r *Registry) BackendFor(name string) string {
	if m, ok := r.cfg.Models[name]; ok {
		return m.Backend
	}
	return ""
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
