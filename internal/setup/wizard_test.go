package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/anthropicauth"
	"github.com/whyrusleeping/ycc/internal/codex"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/openaiauth"
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

// TestWizardOAuthLoginFlow drives the full subscription path (task 0194): pick
// oauth auth on anthropic, Enter routes through the login step (no stored
// credentials), pasting a code exchanges + persists it, verification runs, and
// the finished provider carries auth=oauth into the config.
func TestWizardOAuthLoginFlow(t *testing.T) {
	isolateConfig(t)
	m := newModel()
	m.verify = func(provider) error { return nil }
	m.hasCreds = func() bool { return false }
	m.openURL = func(string) {}
	var gotCode string
	m.exchange = func(code string, p anthropicauth.PKCE) (*anthropicauth.Credentials, error) {
		gotCode = code
		if p.Verifier == "" {
			t.Error("exchange called with empty PKCE verifier")
		}
		return &anthropicauth.Credentials{AccessToken: "sk-ant-oat01-x", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Hour).Unix()}, nil
	}

	m = drive(m, strings.Split("claude", "")...)
	m.focusField(fieldAuth)
	m = drive(m, "right") // api key -> oauth
	if got := authList[m.authIdx]; got != "oauth" {
		t.Fatalf("auth=%q, want oauth", got)
	}
	m = drive(m, "enter") // -> stepLogin (no creds stored)
	if m.step != stepLogin || m.loginURL == "" {
		t.Fatalf("expected stepLogin with URL, step=%v url=%q", m.step, m.loginURL)
	}
	m = drive(m, strings.Split("the-code#state", "")...)
	m = drive(m, "enter") // exchange + save -> stepVerify
	if gotCode != "the-code#state" {
		t.Fatalf("exchange got code %q", gotCode)
	}
	if m.step != stepVerify {
		t.Fatalf("expected stepVerify after exchange, got %v (loginErr=%q)", m.step, m.loginErr)
	}
	if creds, ok := anthropicauth.Load(); !ok || creds.AccessToken != "sk-ant-oat01-x" {
		t.Fatalf("credentials not persisted: %+v ok=%v", creds, ok)
	}
	m = drive(m, "enter") // accept verify -> addMore
	m = drive(m, "enter") // continue -> roles
	m = drive(m, "enter") // finish
	if !m.completed {
		t.Fatal("wizard not completed")
	}
	cfg, err := buildConfig(m.providers, m.coord, m.impl, m.reviewers)
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	if got := cfg.Models["claude"].Auth; got != "oauth" {
		t.Fatalf("config auth=%q, want oauth", got)
	}
}

// TestWizardOAuthSkipsLoginWhenCredsStored: with credentials already stored the
// oauth path goes straight to verification.
func TestWizardOAuthSkipsLoginWhenCredsStored(t *testing.T) {
	isolateConfig(t)
	m := newModel()
	m.verify = func(provider) error { return nil }
	m.hasCreds = func() bool { return true }
	m.openURL = func(string) { t.Error("browser opened despite stored creds") }
	m = drive(m, strings.Split("claude", "")...)
	m.focusField(fieldAuth)
	m = drive(m, "right")
	m = drive(m, "enter")
	if m.step != stepVerify {
		t.Fatalf("expected stepVerify, got %v", m.step)
	}
}

// TestWizardOAuthLoginEscBacksOut: esc in the login step returns to the editor
// (retaining fields) instead of skipping the wizard.
func TestWizardOAuthLoginEscBacksOut(t *testing.T) {
	isolateConfig(t)
	m := newModel()
	m.hasCreds = func() bool { return false }
	m.openURL = func(string) {}
	m = drive(m, strings.Split("claude", "")...)
	m.focusField(fieldAuth)
	m = drive(m, "right")
	m = drive(m, "enter")
	if m.step != stepLogin {
		t.Fatalf("expected stepLogin, got %v", m.step)
	}
	m = drive(m, "esc")
	if m.step != stepProvider || m.skipped {
		t.Fatalf("expected back to editor, step=%v skipped=%v", m.step, m.skipped)
	}
	if got := m.inputs[fieldName].Value(); got != "claude" {
		t.Fatalf("name not retained after esc: %q", got)
	}
}

// TestWizardAuthSupportedBackendsOnly: the auth picker is pinned to api-key
// on backends without subscription support, and switching to one resets a
// selected oauth. anthropic → openai keeps the selection (both supported).
func TestWizardAuthSupportedBackendsOnly(t *testing.T) {
	m := newModel()
	m.focusField(fieldAuth)
	m = drive(m, "right")
	if got := authList[m.authIdx]; got != "oauth" {
		t.Fatalf("auth=%q, want oauth on anthropic", got)
	}
	// anthropic -> openai keeps oauth; -> ollama resets and no longer cycles.
	m.focusField(fieldBackend)
	m = drive(m, "right")
	if got := authList[m.authIdx]; got != "oauth" {
		t.Fatalf("auth=%q after anthropic->openai, want oauth kept", got)
	}
	m = drive(m, "right") // openai -> ollama
	if got := authList[m.authIdx]; got != "" {
		t.Fatalf("auth=%q after switch to ollama, want api-key default", got)
	}
	m.focusField(fieldAuth)
	m = drive(m, "right")
	if got := authList[m.authIdx]; got != "" {
		t.Fatalf("auth cycled on ollama backend: %q", got)
	}
}

// TestWizardOpenAIOAuthLoginFlow drives the ChatGPT subscription path (task
// 0195): openai backend + oauth auth, Enter starts the browser-wait login
// (injected), completion persists credentials and proceeds to verification,
// and the finished provider carries auth=oauth into the config.
func TestWizardOpenAIOAuthLoginFlow(t *testing.T) {
	isolateConfig(t)
	m := newModel()
	m.verify = func(provider) error { return nil }
	m.hasOpenAICred = func() bool { return false }
	m.openURL = func(string) {}
	m.openaiLogin = func(ctx context.Context, onURL func(string)) (*openaiauth.Credentials, error) {
		onURL("https://auth.openai.example/authorize")
		return &openaiauth.Credentials{
			AccessToken: "tok", RefreshToken: "rt", AccountID: "acct-1",
			ExpiresAt: time.Now().Add(time.Hour).Unix(),
		}, nil
	}

	m = drive(m, strings.Split("codex", "")...)
	// Switch backend to openai; the still-default model id re-seeds to the
	// codex default when oauth is selected.
	m.focusField(fieldBackend)
	m = drive(m, "right") // anthropic -> openai
	m.focusField(fieldAuth)
	m = drive(m, "right") // api key -> oauth
	if got := m.inputs[fieldModel].Value(); got != codex.Models[0] {
		t.Fatalf("model id = %q, want codex default %q", got, codex.Models[0])
	}
	m = drive(m, "enter") // -> stepLogin in wait mode; injected login resolves via cmds
	if m.step != stepVerify {
		t.Fatalf("expected stepVerify after injected login, got step=%v wait=%v err=%q", m.step, m.loginWait, m.loginErr)
	}
	if creds, ok := openaiauth.Load(); !ok || creds.AccountID != "acct-1" {
		t.Fatalf("credentials not persisted: %+v ok=%v", creds, ok)
	}
	m = drive(m, "enter") // accept verify -> addMore
	m = drive(m, "enter") // continue -> roles
	m = drive(m, "enter") // finish
	if !m.completed {
		t.Fatal("wizard not completed")
	}
	cfg, err := buildConfig(m.providers, m.coord, m.impl, m.reviewers)
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	mdl := cfg.Models["codex"]
	if mdl.Auth != "oauth" || mdl.Backend != "openai" {
		t.Fatalf("config model = %+v, want openai oauth", mdl)
	}
}

// TestWizardOpenAIOAuthLoginError: a failed browser login shows the error on
// the login screen and esc returns to the editor.
func TestWizardOpenAIOAuthLoginError(t *testing.T) {
	isolateConfig(t)
	m := newModel()
	m.hasOpenAICred = func() bool { return false }
	m.openURL = func(string) {}
	m.openaiLogin = func(ctx context.Context, onURL func(string)) (*openaiauth.Credentials, error) {
		return nil, errors.New("login timed out")
	}
	m = drive(m, strings.Split("codex", "")...)
	m.focusField(fieldBackend)
	m = drive(m, "right") // -> openai
	m.focusField(fieldAuth)
	m = drive(m, "right") // -> oauth
	m = drive(m, "enter")
	if m.step != stepLogin || m.loginErr == "" {
		t.Fatalf("expected login error surfaced, step=%v err=%q", m.step, m.loginErr)
	}
	m = drive(m, "esc")
	if m.step != stepProvider || m.skipped {
		t.Fatalf("expected back to editor, step=%v skipped=%v", m.step, m.skipped)
	}
}
