package setup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/secrets"
)

// isolateConfig points os.UserConfigDir at a temp dir so secrets/config writes
// stay hermetic (mirrors internal/secrets/secrets_test.go).
func isolateConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("ANTHROPIC_API_KEY", "")
}

func TestNeedsSetupSecretsStore(t *testing.T) {
	isolateConfig(t)
	ws := t.TempDir()

	if !NeedsSetup(ws) {
		t.Fatalf("expected NeedsSetup true with no config, no env key, no secret")
	}

	if err := secrets.Set("ANTHROPIC_API_KEY", "sk-stored"); err != nil {
		t.Fatalf("secrets.Set: %v", err)
	}
	if NeedsSetup(ws) {
		t.Fatalf("expected NeedsSetup false when a secrets-store token is present")
	}
}

// enterEditor types a name then focuses through to the api-key field and types
// the key, returning the model still in stepProvider.
func fillProvider(m model, name, key string) model {
	m = drive(m, strings.Split(name, "")...)
	// focus: name -> ... -> key (fieldKey is the last field)
	m.focusField(fieldKey)
	m = drive(m, strings.Split(key, "")...)
	return m
}

func TestWizardCapturesKeyOnProvider(t *testing.T) {
	m := newModel()
	m.verify = func(provider) error { return nil }
	m = fillProvider(m, "claude", "sk-secret-123")
	m = drive(m, "enter") // -> stepVerify (pass)
	if m.step != stepVerify {
		t.Fatalf("expected stepVerify, got %v", m.step)
	}
	m = drive(m, "enter") // accept
	if len(m.providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(m.providers))
	}
	if got := m.providers[0].key; got != "sk-secret-123" {
		t.Fatalf("pasted key not captured: %q", got)
	}
}

func TestWizardKeyNeverInConfig(t *testing.T) {
	m := newModel()
	m.verify = func(provider) error { return nil }
	m = fillProvider(m, "claude", "sk-super-secret")
	m = drive(m, "enter") // verify
	m = drive(m, "enter") // accept -> addMore
	m = drive(m, "enter") // continue -> roles
	m = drive(m, "enter") // finish
	if !m.completed {
		t.Fatalf("expected completed wizard")
	}
	cfg, err := buildConfig(m.providers, m.coord, m.impl, m.reviewers)
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	path := filepath.Join(t.TempDir(), "ycc.toml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data), "sk-super-secret") {
		t.Fatalf("config file leaked the API key:\n%s", data)
	}
}

func TestWizardVerifyRetry(t *testing.T) {
	calls := 0
	m := newModel()
	m.verify = func(provider) error {
		calls++
		if calls == 1 {
			return errors.New("boom")
		}
		return nil
	}
	m = drive(m, "claude")
	m = drive(m, "enter") // verify -> fail
	if m.step != stepVerify || m.verifyErr == nil {
		t.Fatalf("expected failed verify, step=%v err=%v", m.step, m.verifyErr)
	}
	m = drive(m, "r") // retry -> pass
	if m.verifyErr != nil {
		t.Fatalf("expected retry to pass, got %v", m.verifyErr)
	}
	if calls != 2 {
		t.Fatalf("expected 2 verify calls, got %d", calls)
	}
	m = drive(m, "enter") // accept
	if m.step != stepAddMore || len(m.providers) != 1 {
		t.Fatalf("expected addMore with 1 provider, step=%v n=%d", m.step, len(m.providers))
	}
}

func TestWizardVerifyEditRetainsValues(t *testing.T) {
	m := newModel()
	m.verify = func(provider) error { return errors.New("nope") }
	m = fillProvider(m, "claude", "sk-keep-me")
	m = drive(m, "enter") // verify -> fail
	if m.verifyErr == nil {
		t.Fatalf("expected failed verify")
	}
	m = drive(m, "e") // edit -> back to provider editor
	if m.step != stepProvider {
		t.Fatalf("expected stepProvider after edit, got %v", m.step)
	}
	if got := m.inputs[fieldName].Value(); got != "claude" {
		t.Fatalf("name not retained: %q", got)
	}
	if got := m.inputs[fieldKey].Value(); got != "sk-keep-me" {
		t.Fatalf("key not retained: %q", got)
	}
	if len(m.providers) != 0 {
		t.Fatalf("provider should not be committed on edit, got %d", len(m.providers))
	}
}

func TestWizardVerifyAcceptAnyway(t *testing.T) {
	m := newModel()
	m.verify = func(provider) error { return errors.New("unreachable") }
	m = drive(m, "claude")
	m = drive(m, "enter") // verify -> fail
	if m.verifyErr == nil {
		t.Fatalf("expected failed verify")
	}
	m = drive(m, "enter") // accept anyway
	if m.step != stepAddMore || len(m.providers) != 1 {
		t.Fatalf("expected addMore with 1 provider, step=%v n=%d", m.step, len(m.providers))
	}
}

func TestWizardCyclePresetFillsModel(t *testing.T) {
	m := newModel()
	// focus the model field and cycle curated ids (no fetch => curated source).
	m.focusField(fieldModel)
	curated := config.CuratedModelIDs("anthropic")
	m = drive(m, "ctrl+n") // -> index 0
	if got := m.inputs[fieldModel].Value(); got != curated[0] {
		t.Fatalf("ctrl+n want %q, got %q", curated[0], got)
	}
	m = drive(m, "ctrl+n") // -> index 1
	if got := m.inputs[fieldModel].Value(); got != curated[1] {
		t.Fatalf("ctrl+n want %q, got %q", curated[1], got)
	}
	m = drive(m, "ctrl+p") // -> index 0
	if got := m.inputs[fieldModel].Value(); got != curated[0] {
		t.Fatalf("ctrl+p want %q, got %q", curated[0], got)
	}
}

func TestWizardDiscoverFetchesIDs(t *testing.T) {
	m := newModel()
	m.discover = func(provider) ([]string, error) {
		return []string{"zeta", "alpha"}, nil
	}
	m.focusField(fieldModel)
	m = drive(m, "ctrl+f")
	if len(m.fetchedIDs) != 2 {
		t.Fatalf("expected 2 fetched ids, got %#v", m.fetchedIDs)
	}
	m = drive(m, "ctrl+n")
	if got := m.inputs[fieldModel].Value(); got != "zeta" {
		t.Fatalf("expected fetched id cycled in, got %q", got)
	}
}
