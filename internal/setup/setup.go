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

	tea "github.com/charmbracelet/bubbletea"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/daemon"
)

// NeedsSetup reports whether the first-run wizard should run: there is no
// discoverable ycc.toml (workspace or user config dir) AND no fallback env key
// (ANTHROPIC_API_KEY) is set. When either exists we can reach a model already,
// so the wizard is skipped and ycc goes straight to the home menu.
func NeedsSetup(workspace string) bool {
	return daemon.DiscoverConfig(workspace) == "" && os.Getenv("ANTHROPIC_API_KEY") == ""
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

func defaultModel(backend string) string {
	switch backend {
	case "anthropic":
		return "claude-opus-4-8"
	case "openai":
		return "gpt-4o"
	case "ollama":
		return "llama3"
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
		MaxTokens: 8192,
	}, nil
}

// Run launches the wizard. It returns the path of the written config on
// completion, or "" when the user skipped (or on a non-fatal write error, so the
// caller falls back rather than crashing). A non-empty path means "use it".
func Run(workspace string) (string, error) {
	prog := tea.NewProgram(newModel(), tea.WithAltScreen())
	out, err := prog.Run()
	if err != nil {
		return "", err
	}
	m, ok := out.(model)
	if !ok || m.skipped || !m.completed {
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
	return path, nil
}
