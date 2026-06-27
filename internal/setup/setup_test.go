package setup

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/whyrusleeping/ycc/internal/config"
)

func TestNeedsSetup(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("ANTHROPIC_API_KEY", "")

	ws := t.TempDir()
	if !NeedsSetup(ws) {
		t.Fatalf("expected NeedsSetup true for empty workspace and no env key")
	}

	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	if NeedsSetup(ws) {
		t.Fatalf("expected NeedsSetup false when ANTHROPIC_API_KEY is set")
	}
	t.Setenv("ANTHROPIC_API_KEY", "")

	// Write a ycc.toml into the workspace dir -> discoverable -> no setup.
	cfg := config.DefaultAnthropic("https://api.anthropic.com", "m", "ANTHROPIC_API_KEY", 8192)
	if err := config.Save(filepath.Join(ws, "ycc.toml"), cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	if NeedsSetup(ws) {
		t.Fatalf("expected NeedsSetup false when a ycc.toml exists in workspace")
	}
}

func TestBuildConfigRoundTrip(t *testing.T) {
	providers := []provider{
		{name: "claude", backend: "anthropic", baseURL: "https://api.anthropic.com", model: "claude-opus-4-8", keyEnv: "ANTHROPIC_API_KEY"},
		{name: "gpt", backend: "openai", baseURL: "https://api.openai.com/v1", model: "gpt-4o", keyEnv: "OPENAI_API_KEY"},
	}
	cfg, err := buildConfig(providers, "claude", "gpt", []string{"claude", "gpt"})
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "ycc.toml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(loaded.Models, cfg.Models) {
		t.Fatalf("models mismatch: %#v vs %#v", loaded.Models, cfg.Models)
	}
	if loaded.Roles.Coordinator != "claude" || loaded.Roles.Implementer != "gpt" {
		t.Fatalf("roles mismatch: %#v", loaded.Roles)
	}
	if !reflect.DeepEqual(loaded.Roles.Reviewers, []string{"claude", "gpt"}) {
		t.Fatalf("reviewers mismatch: %#v", loaded.Roles.Reviewers)
	}
}

func TestSingleProviderRoleDefaulting(t *testing.T) {
	providers := []provider{
		{name: "claude", backend: "anthropic", baseURL: "https://api.anthropic.com", model: "claude-opus-4-8", keyEnv: "ANTHROPIC_API_KEY"},
	}
	cfg, err := buildConfig(providers, "", "", nil)
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	if cfg.Roles.Coordinator != "claude" || cfg.Roles.Implementer != "claude" {
		t.Fatalf("expected coord/impl default to claude, got %#v", cfg.Roles)
	}
	if !reflect.DeepEqual(cfg.Roles.Reviewers, []string{"claude"}) {
		t.Fatalf("expected reviewers default to [claude], got %#v", cfg.Roles.Reviewers)
	}
	path := filepath.Join(t.TempDir(), "ycc.toml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := config.Load(path); err != nil {
		t.Fatalf("load: %v", err)
	}
}

func TestBuildConfigNoProviders(t *testing.T) {
	if _, err := buildConfig(nil, "", "", nil); err == nil {
		t.Fatalf("expected error for zero providers")
	}
}

// drive feeds key strings to the model and returns the resulting model.
func drive(m model, keys ...string) model {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "left":
			msg = tea.KeyMsg{Type: tea.KeyLeft}
		case "right":
			msg = tea.KeyMsg{Type: tea.KeyRight}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "space":
			msg = tea.KeyMsg{Type: tea.KeySpace}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		out, _ := m.Update(msg)
		m = out.(model)
	}
	return m
}

func TestWizardInteraction(t *testing.T) {
	m := newModel()
	// Type the provider name then accept all default fields and Enter.
	m = drive(m, "claude")
	// fields name/backend default to anthropic; defaults already populate
	// base_url/model/key_env. Enter validates and advances to addMore.
	m = drive(m, "enter")
	if m.step != stepAddMore {
		t.Fatalf("expected stepAddMore, got %v (err=%q)", m.step, m.editErr)
	}
	if len(m.providers) != 1 || m.providers[0].name != "claude" {
		t.Fatalf("expected 1 provider 'claude', got %#v", m.providers)
	}
	// Default cursor is "continue"; Enter goes to roles.
	m = drive(m, "enter")
	if m.step != stepRoles {
		t.Fatalf("expected stepRoles, got %v", m.step)
	}
	// Single provider: reviewers pre-selected. Just Enter to finish.
	m = drive(m, "enter")
	if !m.completed || m.skipped {
		t.Fatalf("expected completed (not skipped): completed=%v skipped=%v", m.completed, m.skipped)
	}
	cfg, err := buildConfig(m.providers, m.coord, m.impl, m.reviewers)
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	path := filepath.Join(t.TempDir(), "ycc.toml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := config.Load(path); err != nil {
		t.Fatalf("load: %v", err)
	}
}

func TestWizardEscSkips(t *testing.T) {
	m := newModel()
	m = drive(m, "claude")
	m = drive(m, "esc")
	if !m.skipped {
		t.Fatalf("expected skipped after esc")
	}
	if m.completed {
		t.Fatalf("did not expect completed after esc")
	}
}

func TestWizardBackendDefaults(t *testing.T) {
	m := newModel()
	// Move focus to backend field and cycle to openai.
	m = drive(m, "tab") // focus backend
	if m.focus != fieldBackend {
		t.Fatalf("expected focus on backend, got %d", m.focus)
	}
	m = drive(m, "right") // anthropic -> openai
	if backends[m.backendIdx] != "openai" {
		t.Fatalf("expected openai backend, got %s", backends[m.backendIdx])
	}
	if got := m.inputs[fieldBaseURL].Value(); got != defaultBaseURL("openai") {
		t.Fatalf("expected base url refreshed to openai default, got %q", got)
	}
	if got := m.inputs[fieldKeyEnv].Value(); got != "OPENAI_API_KEY" {
		t.Fatalf("expected key env OPENAI_API_KEY, got %q", got)
	}
}

func TestConfigPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	p, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	want := filepath.Join(tmp, "ycc", "ycc.toml")
	if p != want {
		t.Fatalf("ConfigPath = %q, want %q", p, want)
	}
	// sanity: the directory under config home does not yet exist
	if _, err := os.Stat(filepath.Dir(p)); !os.IsNotExist(err) {
		// not an assertion failure, just ensure no panic
		_ = err
	}
}
