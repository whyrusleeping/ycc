package daemon

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

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

// In-process sessions outlive the StartSession RPC that launched them, so HTTP
// shutdown alone cannot cancel their inference. The daemon must reclaim the live
// manager sessions, which cancels the engine context and tears down model HTTP.
func TestInProcessShutdownCancelsInflightModelRequest(t *testing.T) {
	requestStarted := make(chan struct{}, 1)
	requestCanceled := make(chan struct{}, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		requestStarted <- struct{}{}
		<-r.Context().Done()
		requestCanceled <- struct{}{}
	}))
	defer backend.Close()

	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "ycc.toml")
	cfg := fmt.Sprintf(`
max_tokens = 64

[models.local]
backend = "openai"
base_url = %q
model = "blocking-test"

[roles]
coordinator = "local"
implementer = "local"
reviewers = ["local"]
`, backend.URL)
	if err := os.WriteFile(configPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	ip, err := StartInProcess(Options{Workspace: workspace, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("StartInProcess: %v", err)
	}
	client := DialClient(ip.Addr, "")
	resp, err := client.StartSession(context.Background(), connect.NewRequest(&v1.StartSessionRequest{
		Workspace: workspace,
		Mode:      "chat",
		Prompt:    "wait for the backend",
	}))
	if err != nil {
		_ = ip.Close()
		t.Fatalf("StartSession: %v", err)
	}

	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		_ = ip.Close()
		t.Fatal("model request did not reach blocking backend")
	}

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- ip.Shutdown() }()
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown did not return promptly")
	}
	select {
	case <-requestCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("backend did not observe model request cancellation")
	}

	// Daemon reclamation is not a user hard-stop: it must leave the session log
	// reopenable without appending a session_stopped marker.
	data, err := os.ReadFile(filepath.Join(workspace, ".ycc", "sessions", resp.Msg.SessionId, "events.jsonl"))
	if err != nil {
		t.Fatalf("read session log: %v", err)
	}
	if strings.Contains(string(data), `"type":"session_stopped"`) {
		t.Fatalf("shutdown wrote session_stopped marker: %s", data)
	}
}
