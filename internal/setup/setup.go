// Package setup implements the first-run setup wizard (spec §19.1). The first
// time a user runs `ycc` with no usable model configuration and no fallback env
// key, the wizard guides them through configuring one or more model providers
// and assigning workflow roles, then writes ~/.config/ycc/ycc.toml via
// config.Save. The written path is fed into daemon resolution so the first real
// session uses it instead of falling back to a keyless config that 401s.
//
// This is a client/TUI form (Bubble Tea), NOT an agent flow — it must work
// before any working model exists. It is intentionally skippable (a user may
// hand-author ycc.toml); skipping proceeds with the existing fallback behaviour.
package setup

import (
	"fmt"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/daemon"
	"github.com/whyrusleeping/ycc/internal/secrets"
)

// NeedsSetup reports whether the first-run wizard should run: there is no
// discoverable ycc.toml (workspace or user config dir) AND no fallback API key
// is reachable. A key is considered reachable when ANTHROPIC_API_KEY is set in
// the environment or a token is present in the machine-local secrets store
// (saved via `ycc token set`); either way we can hit a model already, so the
// wizard is skipped and ycc goes straight to the home menu.
func NeedsSetup(workspace string) bool {
	if daemon.DiscoverConfig(workspace) != "" {
		return false
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		return false
	}
	if _, ok := secrets.Lookup("ANTHROPIC_API_KEY"); ok {
		return false
	}
	return true
}

// ConfigPath returns the path the wizard writes to: the user config dir
// candidate (~/.config/ycc/ycc.toml) that DiscoverConfig looks for second.
func ConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ycc", "ycc.toml"), nil
}

// backends lists the supported provider backends in cycle order.
var backends = []string{"anthropic", "openai", "ollama"}

func defaultBaseURL(backend string) string {
	switch backend {
	case "anthropic":
		return "https://api.anthropic.com"
	case "openai":
		return "https://api.openai.com/v1"
	case "ollama":
		return "http://localhost:11434"
	}
	return ""
}

func defaultKeyEnv(backend string) string {
	switch backend {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "ollama":
		return ""
	}
	return ""
}

// defaultModel returns the first curated model id for a backend, reusing the
// same curated per-backend defaults the TUI backend manager seeds (spec §13).
func defaultModel(backend string) string {
	if ids := config.CuratedModelIDs(backend); len(ids) > 0 {
		return ids[0]
	}
	return ""
}

// provider is one collected provider entry before it becomes a config.Model.
type provider struct {
	name    string
	backend string
	baseURL string
	model   string
	keyEnv  string
	key     string // pasted API key value (stored in secrets, never in ycc.toml)
	auth    string // credential mechanism: "" (api-key default) | "oauth" (spec §13)
}

// buildConfig turns the collected providers and role choices into a *config.Config.
// It is pure and testable; it does not write or fully validate (config.Save
// validates at write time). It enforces at least one provider, unique non-empty
// provider names, and applies single-provider role defaulting.
func buildConfig(providers []provider, coord, impl string, reviewers []string) (*config.Config, error) {
	if len(providers) == 0 {
		return nil, fmt.Errorf("setup: at least one provider is required")
	}
	models := make(map[string]config.Model, len(providers))
	for _, p := range providers {
		if p.name == "" {
			return nil, fmt.Errorf("setup: provider name must not be empty")
		}
		if _, dup := models[p.name]; dup {
			return nil, fmt.Errorf("setup: duplicate provider name %q", p.name)
		}
		models[p.name] = config.Model{
			Backend: p.backend,
			BaseURL: p.baseURL,
			Model:   p.model,
			KeyEnv:  p.keyEnv,
			Auth:    p.auth,
		}
	}
	if coord == "" {
		coord = providers[0].name
	}
	if impl == "" {
		impl = providers[0].name
	}
	if len(reviewers) == 0 {
		reviewers = []string{providers[0].name}
	}
	return &config.Config{
		Models:    models,
		Roles:     config.Roles{Coordinator: coord, Implementer: impl, Reviewers: reviewers},
		MaxTokens: config.DefaultMaxTokens,
	}, nil
}

// Run launches the wizard. It returns the path of the written config on
// completion, or "" when the user skipped (or on a non-fatal write error, so the
// caller falls back rather than crashing). A non-empty path means "use it".
func Run(workspace string) (string, error) {
	prog := tea.NewProgram(newModel())
	out, err := prog.Run()
	if err != nil {
		return "", err
	}
	m, ok := out.(model)
	if !ok || m.skipped || !m.completed {
		printSkipGuidance()
		return "", nil
	}
	cfg, err := buildConfig(m.providers, m.coord, m.impl, m.reviewers)
	if err != nil {
		// Don't crash the app — fall back to the prior keyless behaviour.
		fmt.Fprintf(os.Stderr, "ycc: setup: %v; continuing with fallback\n", err)
		return "", nil
	}
	path, err := ConfigPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ycc: setup: %v; continuing with fallback\n", err)
		return "", nil
	}
	if err := config.Save(path, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "ycc: setup: write %s: %v; continuing with fallback\n", path, err)
		return "", nil
	}
	// Persist any pasted API keys to the machine-local secrets store (never into
	// ycc.toml). Best-effort: warn on failure but keep the written config.
	for _, p := range m.providers {
		if p.keyEnv == "" || p.key == "" {
			continue
		}
		if err := secrets.Set(p.keyEnv, p.key); err != nil {
			fmt.Fprintf(os.Stderr, "ycc: setup: storing key for %s failed: %v\n", p.keyEnv, err)
		}
	}
	return path, nil
}

// printSkipGuidance tells a user who skipped the wizard how to reach a working
// model, so they don't silently proceed toward a keyless 401.
func printSkipGuidance() {
	fmt.Fprint(os.Stderr, "ycc: setup skipped — to get a working model either:\n"+
		"  export ANTHROPIC_API_KEY=sk-...\n"+
		"  or run: ycc token set ANTHROPIC_API_KEY\n"+
		"  or re-run setup later from esc → settings\n")
}
