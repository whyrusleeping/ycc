package config

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/anthropicauth"
	"github.com/whyrusleeping/ycc/internal/codex"
	"github.com/whyrusleeping/ycc/internal/openaiauth"
)

// isolateSecrets points the secrets store at a temp dir so tests never touch
// the real ~/.config/ycc/secrets.json.
func isolateSecrets(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
}

func TestValidateAuth(t *testing.T) {
	cases := []struct {
		name    string
		m       Model
		wantErr bool
	}{
		{"oauth on anthropic ok", Model{Backend: "anthropic", Model: "claude-opus-4-8", Auth: "oauth"}, false},
		{"oauth on openai ok", Model{Backend: "openai", Model: "gpt-5.3-codex", Auth: "oauth"}, false},
		{"api-key explicit ok", Model{Backend: "openai", Model: "gpt-5.5", Auth: "api-key"}, false},
		{"default empty ok", Model{Backend: "anthropic", Model: "claude-opus-4-8"}, false},
		{"oauth on ollama rejected", Model{Backend: "ollama", Model: "qwen2.5", Auth: "oauth"}, true},
		{"unknown auth rejected", Model{Backend: "anthropic", Model: "claude-opus-4-8", Auth: "magic"}, true},
	}
	for _, c := range cases {
		err := c.m.Validate("m")
		if (err != nil) != c.wantErr {
			t.Errorf("%s: Validate err = %v, wantErr=%v", c.name, err, c.wantErr)
		}
	}
}

// Subscription usage is prepaid: an oauth model must not pick up built-in
// API-key default pricing (spec §20.4 — never invent numbers), but explicit
// config pricing still wins.
func TestEffectivePricingOAuthUnpriced(t *testing.T) {
	m := Model{Backend: "anthropic", Model: "claude-opus-4-8", Auth: "oauth"}
	if p := m.EffectivePricing(); p.Configured {
		t.Fatalf("oauth model picked up default pricing: %+v", p)
	}
	rate := 1.5
	m.PriceInput = &rate
	if p := m.EffectivePricing(); !p.Configured || p.Input != 1.5 {
		t.Fatalf("explicit pricing should win for oauth models: %+v", p)
	}
	// Sanity: the same model without oauth is default-priced.
	if p := (Model{Backend: "anthropic", Model: "claude-opus-4-8"}).EffectivePricing(); !p.Configured {
		t.Fatal("api-key model unexpectedly unpriced")
	}
}

// anthropicStub is a minimal /v1/messages endpoint that records the request
// headers and returns a well-formed empty completion.
func anthropicStub(t *testing.T, got *http.Header) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got = r.Header.Clone()
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant",
			"content":     []map[string]any{{"type": "text", "text": "ok"}},
			"model":       "claude-opus-4-8",
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
	}))
}

// turnOnce builds the named model and runs a single non-streaming turn against
// the stub so the auth headers actually hit the wire.
func turnOnce(t *testing.T, reg *Registry, name string) {
	t.Helper()
	c, modelID, err := reg.Build(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Turn(gollama.RequestOptions{Model: modelID, Messages: []gollama.Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("Turn: %v", err)
	}
}

func TestBuildAnthropicOAuthHeaders(t *testing.T) {
	isolateSecrets(t)
	var got http.Header
	srv := anthropicStub(t, &got)
	defer srv.Close()

	// Live (non-expired) stored credentials: no refresh needed.
	if err := anthropicauth.Save(&anthropicauth.Credentials{
		AccessToken: "sk-ant-oat01-live", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(&Config{Models: map[string]Model{
		"sub": {Backend: "anthropic", BaseURL: srv.URL, Model: "claude-opus-4-8", Auth: "oauth"},
	}})
	turnOnce(t, reg, "sub")
	if auth := got.Get("Authorization"); auth != "Bearer sk-ant-oat01-live" {
		t.Errorf("Authorization = %q", auth)
	}
	if beta := got.Get("anthropic-beta"); beta != anthropicauth.BetaHeader {
		t.Errorf("anthropic-beta = %q", beta)
	}
	if got.Get("x-api-key") != "" {
		t.Error("oauth request must not carry x-api-key")
	}
}

func TestBuildAnthropicSetupTokenHeaders(t *testing.T) {
	isolateSecrets(t)
	var got http.Header
	srv := anthropicStub(t, &got)
	defer srv.Close()

	// A long-lived `claude setup-token` credential resolved via key_env gets
	// the same bearer + beta treatment, auto-detected by prefix.
	t.Setenv("FAKE_ANTHROPIC_KEY", "sk-ant-oat01-longlived")
	reg := NewRegistry(&Config{Models: map[string]Model{
		"sub": {Backend: "anthropic", BaseURL: srv.URL, Model: "claude-opus-4-8", KeyEnv: "FAKE_ANTHROPIC_KEY"},
	}})
	turnOnce(t, reg, "sub")
	if auth := got.Get("Authorization"); auth != "Bearer sk-ant-oat01-longlived" {
		t.Errorf("Authorization = %q", auth)
	}
	if beta := got.Get("anthropic-beta"); beta != anthropicauth.BetaHeader {
		t.Errorf("anthropic-beta = %q", beta)
	}
	if got.Get("x-api-key") != "" {
		t.Error("setup-token request must not carry x-api-key")
	}

	// A plain API key still travels as x-api-key with no beta header.
	t.Setenv("FAKE_ANTHROPIC_KEY", "sk-ant-api03-regular")
	turnOnce(t, reg, "sub")
	if got.Get("x-api-key") != "sk-ant-api03-regular" || got.Get("Authorization") != "" || got.Get("anthropic-beta") != "" {
		t.Errorf("api-key headers wrong: x-api-key=%q auth=%q beta=%q",
			got.Get("x-api-key"), got.Get("Authorization"), got.Get("anthropic-beta"))
	}
}

func TestBuildOAuthWithoutCredentialsFails(t *testing.T) {
	isolateSecrets(t)
	reg := NewRegistry(&Config{Models: map[string]Model{
		"sub": {Backend: "anthropic", BaseURL: "http://127.0.0.1:0", Model: "claude-opus-4-8", Auth: "oauth"},
	}})
	if _, _, err := reg.Build("sub"); err == nil {
		t.Fatal("expected Build to fail without stored oauth credentials")
	}
}

// TestBuildOpenAIOAuthUsesCodex: an openai model with auth=oauth routes turns
// through the codex Responses transport with subscription headers — and a
// platform-API base_url is swapped for the codex backend (here an explicit
// test URL is honored as a proxy would be).
func TestBuildOpenAIOAuthUsesCodex(t *testing.T) {
	isolateSecrets(t)
	if err := openaiauth.Save(&openaiauth.Credentials{
		AccessToken: "tok-live", RefreshToken: "rt", AccountID: "acct-7",
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	var got http.Header
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		path = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer srv.Close()

	reg := NewRegistry(&Config{Models: map[string]Model{
		"codex": {Backend: "openai", BaseURL: srv.URL, Model: "gpt-5.3-codex", Auth: "oauth"},
	}})
	c, modelID, err := reg.Build("codex")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Turn(gollama.RequestOptions{Model: modelID, Messages: []gollama.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/responses" {
		t.Errorf("path = %q, want /responses", path)
	}
	if got.Get("Authorization") != "Bearer tok-live" || got.Get("chatgpt-account-id") != "acct-7" {
		t.Errorf("auth headers wrong: auth=%q acct=%q", got.Get("Authorization"), got.Get("chatgpt-account-id"))
	}
	if got.Get("OpenAI-Beta") != "responses=experimental" {
		t.Errorf("OpenAI-Beta = %q", got.Get("OpenAI-Beta"))
	}
	if resp.Choices[0].Message.Content != "ok" {
		t.Errorf("content = %q", resp.Choices[0].Message.Content)
	}
}

// TestCodexBaseURLSwap: empty and platform-API base URLs route to the codex
// default; explicit other URLs (proxies) are honored.
func TestCodexBaseURLSwap(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", codex.DefaultBaseURL},
		{"https://api.openai.com/v1", codex.DefaultBaseURL},
		{"https://proxy.example/codex", "https://proxy.example/codex"},
	}
	for _, c := range cases {
		if got := codexBase(c.in); got != c.want {
			t.Errorf("codexBase(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
