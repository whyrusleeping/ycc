package daemon

import "testing"

// TestStartInProcessLifecycle is the core guarantee of the one-shot lifecycle
// (task 0014): the in-process daemon binds an ephemeral loopback port (not the
// well-known persistent one), is reachable while running, and is gone after
// Shutdown — so no detached survivor can linger and go stale.
func TestStartInProcessLifecycle(t *testing.T) {
	ip, err := StartInProcess(Options{
		Workspace: t.TempDir(),
		Model:     "claude-opus-4-8",
		BaseURL:   "https://api.anthropic.com",
		KeyEnv:    "ANTHROPIC_API_KEY",
		MaxTokens: 8192,
	})
	if err != nil {
		t.Fatalf("StartInProcess: %v", err)
	}
	if ip.Addr == LocalAddr {
		t.Fatalf("expected an ephemeral address, got the well-known persistent one %s", ip.Addr)
	}
	if !Reachable(ip.Addr, "") {
		t.Fatalf("in-process daemon not reachable at %s", ip.Addr)
	}
	if err := ip.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if Reachable(ip.Addr, "") {
		t.Fatalf("daemon still reachable at %s after Shutdown", ip.Addr)
	}
}
