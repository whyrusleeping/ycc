// Package config loads the model/role configuration (spec §13) and builds the
// per-role gollama backends the engine uses. A logical model name (e.g. "claude",
// "gpt", "glm", "local") maps to a backend + base URL + model id + key env var;
// roles (coordinator, implementer, reviewers) reference those logical names, so
// review can fan out across genuinely different models/providers.
package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/secrets"
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

// Validate checks a single model record for the minimum fields Build needs. It
// is used by the runtime CRUD path (Registry.UpsertModel) before a model is
// admitted to the live config. name is the logical model name the record is
// stored under; an empty name, empty backend model id, or unsupported backend
// are rejected (the accepted backends mirror Build's switch).
func (m Model) Validate(name string) error {
	if name == "" {
		return errors.New("model name is required")
	}
	if m.Model == "" {
		return fmt.Errorf("model %q: model id is required", name)
	}
	switch m.Backend {
	case "anthropic", "openai", "openai-compatible", "glm", "ollama":
	default:
		return fmt.Errorf("model %q: unsupported backend %q", name, m.Backend)
	}
	return nil
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

// DefaultMaxTokens is the default per-turn output token cap (plumbed to
// gollama.Options.MaxTokens). It is deliberately large because extended-thinking
// budgets are drawn from this same allowance: a small cap can be entirely
// consumed by a reasoning block, cutting the turn off before the model emits any
// tool call. The default model (Claude Opus 4) comfortably supports an output
// cap this size. Used as the shared default across the config default, the
// daemon options, and the CLI flag default so they stay consistent.
const DefaultMaxTokens = 32000

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

// isSelfReviewStrategy reports whether a review tier's strategy means the
// coordinator reviews the change itself (no separate reviewer agent).
func isSelfReviewStrategy(s string) bool {
	switch s {
	case "coordinator", "self", "self-review":
		return true
	default:
		return false
	}
}

// validReviewStrategy reports whether s is a recognized review strategy. The
// empty string is valid and means the default ("agents").
func validReviewStrategy(s string) bool {
	switch s {
	case "", "agents":
		return true
	default:
		return isSelfReviewStrategy(s)
	}
}

// builtinReviewTiers names the tiers that always exist regardless of config.
var builtinReviewTiers = map[string]bool{
	"simple": true, "single-opus": true, "high-powered": true,
}

// effectiveReviewTiers returns the built-in tiers overlaid with any configured
// tiers, plus the effective default tier name. The built-ins guarantee the
// simple/single-opus/high-powered tiers always exist; a configured tier with the
// same name overrides the built-in. Caller must hold the registry lock when
// invoked via the Registry.
func (c *Config) effectiveReviewTiers() (map[string]ReviewTier, string) {
	revs := append([]string(nil), c.Roles.Reviewers...)
	tiers := map[string]ReviewTier{
		"simple":       {Strategy: "coordinator"},
		"single-opus":  {Strategy: "agents", Models: revs},
		"high-powered": {Strategy: "agents", Models: append([]string(nil), revs...)},
	}
	for name, t := range c.Reviews.Tiers {
		tiers[name] = t
	}
	def := "single-opus"
	if c.Reviews.Default != "" {
		def = c.Reviews.Default
	}
	return tiers, def
}

// ReviewTierResolved is the effective tier a coordinator review should use.
type ReviewTierResolved struct {
	Name       string   // effective tier name actually used
	SelfReview bool     // coordinator self-reviews; no reviewer agents
	Models     []string // logical model names for the reviewer fan-out
	Fallback   bool     // requested tier was unknown and we degraded to the default
}

// ReviewTier is one configurable review intensity tier (spec §13). Strategy
// "agents" (the default when empty) spawns reviewer subagents for each logical
// model in Models; "coordinator" (aliases "self"/"self-review") means the
// coordinator reviews the change itself with no separate reviewer agent.
type ReviewTier struct {
	Strategy string   `toml:"strategy"`
	Models   []string `toml:"models"`
}

// Reviews configures the named review tiers and the default tier (spec §13). The
// built-in tiers (simple, single-opus, high-powered) always exist; entries here
// add new tiers or override the built-ins.
type Reviews struct {
	Default string                `toml:"default"`
	Tiers   map[string]ReviewTier `toml:"tiers,omitempty"`
}

// Config is the whole ycc configuration.
type Config struct {
	Models    map[string]Model `toml:"models"`
	Roles     Roles            `toml:"roles"`
	Reviews   Reviews          `toml:"reviews,omitempty"`
	MaxTokens int              `toml:"max_tokens"`
	// MaxTurns caps the number of tool-call turns in a single engine Run as a
	// runaway/cost backstop. 0 means "use the engine's high default" (see
	// engine.defaultMaxTurns). It sits beside max_tokens in the config.
	//
	// Note (task 0010): raising this lets a run accumulate more context, so a
	// very high value can trade a turn-limit abort for a context-window-limit
	// abort until context-window management (0010) lands.
	MaxTurns int `toml:"max_turns"`
	// GC configures automatic reclamation of idle sessions and on-disk session
	// logs (task 0054). All fields default to 0 (disabled) — conservative by
	// default so nothing is reaped or pruned unless explicitly opted in.
	GC GC `toml:"gc,omitempty"`
	// Budget configures optional spend caps that turn the existing usage/cost
	// telemetry into an enforced guard (task 0137, spec §20.6). All fields
	// default to 0 (unlimited) — absent config preserves today's behaviour.
	Budget Budget `toml:"budget,omitempty"`
	// ReadRoots lists additional trusted read-only roots OUTSIDE the workspace
	// that the Read tool may access (task 0068). Defaults (the Go module cache
	// and GOROOT) are always included; these extend them. Writes stay confined
	// to the workspace regardless.
	ReadRoots []string `toml:"read_roots,omitempty"`
	// Notify configures the daemon-side push notifier (task 0142): a best-effort,
	// async webhook (ntfy.sh-compatible) that reaches out when an agent needs the
	// user — questions, idle-with-report, errors, work-loop digests, and blocked
	// implementers. Absent (empty URL) = disabled.
	Notify Notify `toml:"notify,omitempty"`
	// Retry configures automatic retry of transient LLM API failures (task 0133,
	// spec §7.2). All fields default to 0 (unset) — an absent [retry] block keeps
	// today's engine default (engine.DefaultRetryPolicy: 3 attempts, 500ms→30s).
	Retry Retry `toml:"retry,omitempty"`
}

// Retry configures the loop-level retry of transient LLM API failures (task
// 0133, spec §7.2). Each field is 0 when unset, meaning "keep the engine default
// for that field" (see engine.DefaultRetryPolicy). MaxAttempts is the total
// number of attempts including the first; MaxAttempts = 1 disables retry
// entirely. BaseDelayMS is the first backoff step (milliseconds) and doubles
// each attempt; MaxDelayMS caps it.
type Retry struct {
	MaxAttempts int `toml:"max_attempts,omitempty"`
	BaseDelayMS int `toml:"base_delay_ms,omitempty"`
	MaxDelayMS  int `toml:"max_delay_ms,omitempty"`
}

// GC configures the background session reaper (task 0054). IntervalSeconds sets
// how often the reaper runs (0 => a sensible default). IdleTimeoutMinutes stops
// sessions that have been idle (idle status, no pending question, not paused) for
// that long; 0 disables idle reaping. LogRetentionDays prunes on-disk session
// logs older than that many days; 0 disables pruning. Log pruning is OFF by
// default because those logs back the durable session index / history + reopen
// view (tasks 0033/0034) — enabling it discards logs a user could reopen.
type GC struct {
	IntervalSeconds    int `toml:"interval_seconds,omitempty"`
	IdleTimeoutMinutes int `toml:"idle_timeout_minutes,omitempty"`
	LogRetentionDays   int `toml:"log_retention_days,omitempty"`
}

// Budget configures optional spend caps (task 0137, spec §20.6). Session caps are
// enforced daemon-side at safe checkpoints; loop caps are enforced client-side by
// the TUI work-loop driver via GetBudget. Every field defaults to 0 meaning
// "unlimited" so an absent [budget] block preserves the current no-ceiling
// behaviour. Cost caps are in US dollars; token caps count total tokens. A model
// with no configured pricing contributes tokens but no dollars (§20.4), so it can
// only ever breach a token cap — never an invented-dollars cost cap.
type Budget struct {
	SessionCost   float64 `toml:"session_cost,omitempty"`   // $ cap per session (0 = unlimited)
	SessionTokens int64   `toml:"session_tokens,omitempty"` // total-token cap per session (0 = unlimited)
	LoopCost      float64 `toml:"loop_cost,omitempty"`      // $ cap per work-loop run (0 = unlimited)
	LoopTokens    int64   `toml:"loop_tokens,omitempty"`    // total-token cap per work-loop run (0 = unlimited)
}

// Notify configures the daemon-side push notifier (task 0142). URL is the webhook
// endpoint (e.g. https://ntfy.sh/mytopic); empty disables notifications entirely.
// Auth, when set, is sent verbatim as the Authorization request header (e.g.
// "Bearer tk_..."). Events optionally restricts which event kinds fire — an empty
// slice enables all kinds; a non-empty slice enables only the listed kinds (valid
// kinds: question, idle, error, digest, blocked) so autonomous-loop users can pick
// "questions + digest only".
type Notify struct {
	URL    string   `toml:"url,omitempty"`
	Auth   string   `toml:"auth,omitempty"`
	Events []string `toml:"events,omitempty"`
}

// NotifyEventKinds is the set of valid notify.events entries (task 0142). Kept
// here (not in internal/notify) so config validation has no dependency on the
// notifier package.
var NotifyEventKinds = map[string]bool{
	"question": true,
	"idle":     true,
	"error":    true,
	"digest":   true,
	"blocked":  true,
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
	// Validate only the explicitly configured review tiers; the built-ins are
	// always valid. An unknown strategy, an agents-tier referencing an unknown
	// model, or a default naming no tier are configuration errors.
	for name, t := range c.Reviews.Tiers {
		if !validReviewStrategy(t.Strategy) {
			return fmt.Errorf("reviews.tiers.%s: unknown strategy %q", name, t.Strategy)
		}
		if !isSelfReviewStrategy(t.Strategy) {
			for _, mdl := range t.Models {
				if _, ok := c.Models[mdl]; !ok {
					return fmt.Errorf("reviews.tiers.%s: unknown model %q", name, mdl)
				}
			}
		}
	}
	if c.Reviews.Default != "" {
		_, configured := c.Reviews.Tiers[c.Reviews.Default]
		if !configured && !builtinReviewTiers[c.Reviews.Default] {
			return fmt.Errorf("reviews.default: unknown tier %q", c.Reviews.Default)
		}
	}
	if c.GC.IntervalSeconds < 0 || c.GC.IdleTimeoutMinutes < 0 || c.GC.LogRetentionDays < 0 {
		return fmt.Errorf("gc: interval_seconds, idle_timeout_minutes, and log_retention_days must be non-negative")
	}
	if c.Budget.SessionCost < 0 || c.Budget.SessionTokens < 0 || c.Budget.LoopCost < 0 || c.Budget.LoopTokens < 0 {
		return fmt.Errorf("budget: session_cost, session_tokens, loop_cost, and loop_tokens must be non-negative")
	}
	for _, k := range c.Notify.Events {
		if !NotifyEventKinds[k] {
			return fmt.Errorf("notify.events: unknown event kind %q (valid: question, idle, error, digest, blocked)", k)
		}
	}
	if c.Retry.MaxAttempts < 0 || c.Retry.BaseDelayMS < 0 || c.Retry.MaxDelayMS < 0 {
		return fmt.Errorf("retry: max_attempts, base_delay_ms, and max_delay_ms must be non-negative")
	}
	if c.Retry.BaseDelayMS > 0 && c.Retry.MaxDelayMS > 0 && c.Retry.MaxDelayMS < c.Retry.BaseDelayMS {
		return fmt.Errorf("retry: max_delay_ms (%d) must be >= base_delay_ms (%d)", c.Retry.MaxDelayMS, c.Retry.BaseDelayMS)
	}
	return nil
}

// Registry builds backends from a Config. The config is mutable at runtime
// (UpsertModel/RemoveModel) so the settings overlay can add/edit/remove model
// backends without a daemon restart: because Build reads cfg.Models live on
// every call and the same *Registry is shared by every session, a mutation
// takes effect on the next Build/turn/spawn. All access to cfg is guarded by mu.
type Registry struct {
	mu   sync.RWMutex
	cfg  *Config
	path string // discovered config file path for persist=true (empty => cannot persist)
}

// NewRegistry returns a Registry over cfg.
func NewRegistry(cfg *Config) *Registry { return &Registry{cfg: cfg} }

// SetPath records the config file path used when a mutation is persisted
// (persist=true). It is set once at startup; an empty path disables persistence.
func (r *Registry) SetPath(p string) { r.path = p }

// MaxTokens returns the configured per-turn token cap (0 if unset).
func (r *Registry) MaxTokens() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg.MaxTokens
}

// MaxTurns returns the configured per-Run tool-call turn cap (0 if unset, in
// which case the engine applies its high default backstop).
func (r *Registry) MaxTurns() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg.MaxTurns
}

// GC returns the configured GC interval, idle timeout, and log retention as
// durations (each zero when unset/disabled). Guarded by the registry lock like
// MaxTokens/MaxTurns.
func (r *Registry) GC() (interval, idleTimeout, logRetention time.Duration) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return time.Duration(r.cfg.GC.IntervalSeconds) * time.Second,
		time.Duration(r.cfg.GC.IdleTimeoutMinutes) * time.Minute,
		time.Duration(r.cfg.GC.LogRetentionDays) * 24 * time.Hour
}

// Budget returns the configured spend caps (task 0137, spec §20.6). Each field is
// zero when unset/unlimited. Guarded by the registry lock like GC/MaxTokens so a
// runtime config edit is picked up on the next check.
func (r *Registry) Budget() Budget {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg.Budget
}

// Notify returns the configured daemon-side push-notifier settings (task 0142).
// An empty URL means notifications are disabled. Guarded by the registry lock like
// GC/Budget.
func (r *Registry) Notify() Notify {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg.Notify
}

// RetryPolicy returns the loop-level transient-failure retry policy (task 0133,
// spec §7.2). It starts from engine.DefaultRetryPolicy and overlays each nonzero
// [retry] config field (delays converted from milliseconds). Because the result
// always has MaxAttempts >= 1, a configured max_attempts = 1 truly disables retry
// (the loop's "zero value => default" fallback never kicks in). Guarded by the
// registry lock like MaxTokens/MaxTurns so a runtime config edit is picked up on
// the next loop build.
func (r *Registry) RetryPolicy() engine.RetryPolicy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p := engine.DefaultRetryPolicy()
	if r.cfg.Retry.MaxAttempts > 0 {
		p.MaxAttempts = r.cfg.Retry.MaxAttempts
	}
	if r.cfg.Retry.BaseDelayMS > 0 {
		p.BaseDelay = time.Duration(r.cfg.Retry.BaseDelayMS) * time.Millisecond
	}
	if r.cfg.Retry.MaxDelayMS > 0 {
		p.MaxDelay = time.Duration(r.cfg.Retry.MaxDelayMS) * time.Millisecond
	}
	return p
}

// ReadRoots returns a copy of the configured extra trusted read-only roots
// outside the workspace that the Read tool may access (task 0068). The built-in
// defaults (Go module cache, GOROOT) are added separately by the tools layer;
// these are user-configured additions. Writes stay confined to the workspace.
func (r *Registry) ReadRoots() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.cfg.ReadRoots...)
}

// CoordinatorName / ImplementerName / ReviewerNames expose the role assignments.
func (r *Registry) CoordinatorName() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg.Roles.Coordinator
}
func (r *Registry) ImplementerName() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg.Roles.Implementer
}
func (r *Registry) ReviewerNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.cfg.Roles.Reviewers...)
}

// SetRoles updates the default per-role model assignments (roles.coordinator /
// implementer / reviewers) and writes the change back to ycc.toml so a role
// selection made in the settings overlay survives a restart (spec §18.2). Empty
// coordinator/implementer or an empty reviewers slice leaves that role unchanged.
// Every named model must exist. On a persist failure the change is reverted so
// the live config and the file never diverge.
func (r *Registry) SetRoles(coordinator, implementer string, reviewers []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	prev := r.cfg.Roles
	if coordinator != "" {
		if _, ok := r.cfg.Models[coordinator]; !ok {
			return fmt.Errorf("unknown model %q", coordinator)
		}
		r.cfg.Roles.Coordinator = coordinator
	}
	if implementer != "" {
		if _, ok := r.cfg.Models[implementer]; !ok {
			return fmt.Errorf("unknown model %q", implementer)
		}
		r.cfg.Roles.Implementer = implementer
	}
	if len(reviewers) > 0 {
		for _, name := range reviewers {
			if _, ok := r.cfg.Models[name]; !ok {
				return fmt.Errorf("unknown model %q", name)
			}
		}
		r.cfg.Roles.Reviewers = append([]string(nil), reviewers...)
	}
	if err := r.persistLocked(); err != nil {
		r.cfg.Roles = prev
		return err
	}
	return nil
}

// ReviewTier resolves a requested tier name (possibly empty) into the effective
// tier (spec §13). An empty request selects the configured default (not a
// fallback). An unknown non-empty request degrades gracefully to the default
// with Fallback=true.
func (r *Registry) ReviewTier(requested string) ReviewTierResolved {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tiers, def := r.cfg.effectiveReviewTiers()
	name := requested
	fallback := false
	if name == "" {
		name = def
	} else if _, ok := tiers[name]; !ok {
		name, fallback = def, true
	}
	t, ok := tiers[name]
	if !ok { // default itself missing/misconfigured — last-resort safe tier
		return ReviewTierResolved{
			Name:     name,
			Models:   append([]string(nil), r.cfg.Roles.Reviewers...),
			Fallback: fallback,
		}
	}
	return ReviewTierResolved{
		Name:       name,
		SelfReview: isSelfReviewStrategy(t.Strategy),
		Models:     append([]string(nil), t.Models...),
		Fallback:   fallback,
	}
}

// RoleThinking returns the configured per-role thinking level for a role
// ("coordinator"|"implementer"|"reviewers"). ok is false when the role has no
// per-role override configured (so callers fall back to per-model config).
func (r *Registry) RoleThinking(role string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
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

// SetRoleThinking sets the default per-role reasoning level (roles.thinking.* in
// ycc.toml) and persists it so a thinking-level change survives a restart (spec
// §7.4, §18.2). An empty role updates all three roles; a specific role updates
// just that one. The level must be a valid single-knob level (off|low|medium|
// high|xhigh|max). On a persist failure the change is reverted.
func (r *Registry) SetRoleThinking(role, level string) error {
	if !validThinkingLevel(level) {
		return fmt.Errorf("unknown thinking level %q", level)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	prev := r.cfg.Roles.Thinking
	switch role {
	case "":
		r.cfg.Roles.Thinking.Coordinator = level
		r.cfg.Roles.Thinking.Implementer = level
		r.cfg.Roles.Thinking.Reviewers = level
	case RoleCoordinator:
		r.cfg.Roles.Thinking.Coordinator = level
	case RoleImplementer:
		r.cfg.Roles.Thinking.Implementer = level
	case RoleReviewers:
		r.cfg.Roles.Thinking.Reviewers = level
	default:
		return fmt.Errorf("unknown thinking role %q", role)
	}
	if err := r.persistLocked(); err != nil {
		r.cfg.Roles.Thinking = prev
		return err
	}
	return nil
}

// RoleThinkingLevels returns the effective default thinking level for each role,
// resolving unset per-role overrides to the package default (high) so the
// settings overlay can seed its pickers with the real current values.
func (r *Registry) RoleThinkingLevels() (coordinator, implementer, reviewers string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	pick := func(lvl string) string {
		if lvl == "" {
			return defaultEffort
		}
		return lvl
	}
	t := r.cfg.Roles.Thinking
	return pick(t.Coordinator), pick(t.Implementer), pick(t.Reviewers)
}

type ModelInfo struct {
	Name    string
	Backend string
	Model   string
	Pricing Pricing // resolved per-model pricing (spec §20.4); Configured=false ⇒ unpriced
}

// GetModel returns a copy of the model record stored under name (for editing in
// the settings overlay), and whether it is configured.
func (r *Registry) GetModel(name string) (Model, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.cfg.Models[name]
	return m, ok
}

// DiscoverConnModels lists the model ids available from a backend connection by
// resolving key_env locally (env then secrets store) and querying the backend's
// listing endpoint (spec §13). It never returns or logs the secret value. On any
// failure the caller should fall back to CuratedModelIDs(backend).
func (r *Registry) DiscoverConnModels(ctx context.Context, backend, baseURL, keyEnv string) ([]string, error) {
	key := resolveKey(Model{KeyEnv: keyEnv})
	return DiscoverModels(ctx, backend, baseURL, key)
}

// UpsertModel adds or replaces the logical model named name with m, taking
// effect on the next Build (no restart). The record is validated first. When
// persist is true the whole config is written back to the discovered config
// path via Save; if Save fails (or no path is set) the in-memory change is
// reverted so the live config and the file never diverge.
func (r *Registry) UpsertModel(name string, m Model, persist bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := m.Validate(name); err != nil {
		return err
	}
	prev, prevOK := r.cfg.Models[name]
	if r.cfg.Models == nil {
		r.cfg.Models = make(map[string]Model)
	}
	r.cfg.Models[name] = m
	if persist {
		if err := r.persistLocked(); err != nil {
			r.restoreLocked(name, prev, prevOK)
			return err
		}
	}
	return nil
}

// RemoveModel deletes the logical model named name, taking effect on the next
// Build (no restart). It is rejected if a role still references the model (so a
// session can never point at a missing backend). When persist is true the
// change is written back to the config file, reverting on failure.
func (r *Registry) RemoveModel(name string, persist bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	prev, ok := r.cfg.Models[name]
	if !ok {
		return fmt.Errorf("unknown model %q", name)
	}
	var refs []string
	if r.cfg.Roles.Coordinator == name {
		refs = append(refs, RoleCoordinator)
	}
	if r.cfg.Roles.Implementer == name {
		refs = append(refs, RoleImplementer)
	}
	for _, rev := range r.cfg.Roles.Reviewers {
		if rev == name {
			refs = append(refs, RoleReviewers)
			break
		}
	}
	if len(refs) > 0 {
		return fmt.Errorf("cannot remove model %q: still referenced by role(s) %s", name, strings.Join(refs, ", "))
	}
	delete(r.cfg.Models, name)
	if persist {
		if err := r.persistLocked(); err != nil {
			r.cfg.Models[name] = prev
			return err
		}
	}
	return nil
}

// persistLocked writes the current config to r.path. Caller must hold r.mu. If
// no config path is known it is a no-op (nil) rather than an error: a runtime
// edit should always take effect in-memory even when there is nowhere on disk
// to write it (the common paths always resolve a path — see daemon.persistPath).
func (r *Registry) persistLocked() error {
	if r.path == "" {
		return nil
	}
	if err := Save(r.path, r.cfg); err != nil {
		return fmt.Errorf("persist config: %w", err)
	}
	return nil
}

// restoreLocked reverts an upsert: restore the prior record or delete the entry
// if there was none. Caller must hold r.mu.
func (r *Registry) restoreLocked(name string, prev Model, prevOK bool) {
	if prevOK {
		r.cfg.Models[name] = prev
	} else {
		delete(r.cfg.Models, name)
	}
}

// ThinkingFor returns the resolved reasoning settings for a logical model name,
// applying defaults for unset fields. Unknown names return the package defaults
// so callers always get reasoning-on behaviour.
func (r *Registry) ThinkingFor(name string) Thinking {
	r.mu.RLock()
	defer r.mu.RUnlock()
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
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.cfg.Models[name]
	if !ok {
		return Pricing{}
	}
	return m.Pricing()
}

// Has reports whether a logical model name is configured.
func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.cfg.Models[name]
	return ok
}

// BackendFor returns the logical backend family ("anthropic", "openai", ...) for
// a configured model name, or "" if unknown. Used to label per-turn usage events
// (spec §20.1) with the backend that produced them.
func (r *Registry) BackendFor(name string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if m, ok := r.cfg.Models[name]; ok {
		return m.Backend
	}
	return ""
}

// Models returns the configured logical models sorted by name so the settings
// overlay can populate the per-role pickers (spec §13, §18.2).
func (r *Registry) Models() []ModelInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.cfg.Models))
	for name := range r.cfg.Models {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]ModelInfo, 0, len(names))
	for _, name := range names {
		m := r.cfg.Models[name]
		out = append(out, ModelInfo{Name: name, Backend: m.Backend, Model: m.Model, Pricing: m.Pricing()})
	}
	return out
}

// resolveKey returns the API credential for a model following a documented
// precedence: an explicit env var wins (e.g. a CI/one-off override), then a
// token stored in the machine-local secrets store (so a token can be saved once
// instead of exported every session). An empty result is left empty — auth
// failure stays deferred to the API call — to preserve the existing keyless
// Build behavior for backends/tests that build without a key set.
func resolveKey(m Model) string {
	if m.KeyEnv == "" {
		return ""
	}
	if v := os.Getenv(m.KeyEnv); v != "" {
		return v
	}
	if v, ok := secrets.Lookup(m.KeyEnv); ok {
		return v
	}
	return ""
}

// Build constructs a fresh backend client and returns it with its model id. A new
// client per call avoids shared-state races across concurrent subagents.
func (r *Registry) Build(name string) (engine.Turner, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.cfg.Models[name]
	if !ok {
		return nil, "", fmt.Errorf("unknown model %q", name)
	}
	c := gollama.NewClient(m.BaseURL)
	key := resolveKey(m)
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
	// Retry of transient API failures is handled by the engine loop itself
	// (engine.Loop.runTurn, spec §7.2) — ctx-aware and visible to live
	// subscribers — so the client is returned unwrapped.
	return c, m.Model, nil
}
