package daemon

import (
	"strings"
	"testing"
)

// TestServeRefusesNonLoopbackWithoutToken asserts the spec §12/§14 guardrail:
// binding a non-loopback address with no bearer token is refused before the
// listener is ever opened, so an unauthenticated daemon can never be exposed on
// the network. The refusal happens after buildHandler and before
// ListenAndServe, so a minimal valid Options reaches the check without binding.
func TestServeRefusesNonLoopbackWithoutToken(t *testing.T) {
	err := Serve(Options{
		Addr:      "0.0.0.0:0",
		Workspace: t.TempDir(),
		Model:     "claude-opus-4-8",
		BaseURL:   "https://api.anthropic.com",
		KeyEnv:    "ANTHROPIC_API_KEY",
		MaxTokens: 8192,
	})
	if err == nil {
		t.Fatal("Serve on 0.0.0.0 without a token returned nil; want refusal")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Fatalf("error = %q, want it to mention the missing token", err)
	}
}

// TestIsLoopback pins the loopback classification that gates the guardrail:
// loopback binds are allowed token-free; everything else requires a token.
func TestIsLoopback(t *testing.T) {
	loopback := []string{
		"127.0.0.1:8787",
		"localhost:8787",
		"::1:8787",
		"[::1]:8787",
		"127.0.0.1",
	}
	for _, a := range loopback {
		if !isLoopback(a) {
			t.Errorf("isLoopback(%q) = false, want true", a)
		}
	}
	nonLoopback := []string{
		"0.0.0.0:8787",
		":8787", // empty host => wildcard bind (all interfaces), network-exposed
		"",      // empty addr => ":http" wildcard bind, network-exposed
		"192.168.1.10:8787",
		"100.64.0.1:8787", // tailnet-style CGNAT address
		"example.com:8787",
	}
	for _, a := range nonLoopback {
		if isLoopback(a) {
			t.Errorf("isLoopback(%q) = true, want false", a)
		}
	}
}
