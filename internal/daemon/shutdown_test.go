package daemon

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/server"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// testBuildHandler builds the one-shot shape (no admission gate) for tests that
// exercise the handler directly.
func testBuildHandler(o Options) (http.Handler, *session.Manager, error) {
	parts, err := buildHandler(o, nil)
	if err != nil {
		return nil, nil, err
	}
	return parts.handler, parts.mgr, nil
}

// persistentOptions is a persistent loopback daemon confined to temp state and
// config directories, generating its local token like `ycc daemon`.
func persistentOptions(t *testing.T, addr string) Options {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return Options{
		Addr:       addr,
		Workspace:  t.TempDir(),
		Model:      "claude-opus-4-8",
		BaseURL:    "http://127.0.0.1:1",
		KeyEnv:     "YCC_TEST_UNSET_KEY",
		MaxTokens:  8192,
		LocalToken: true,
		Persist:    true,
	}
}

// stateEntries lists the daemon state directory's contents.
func stateEntries(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(StateLockFile()))
	if err != nil {
		t.Fatalf("read state dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// A second daemon on the same state directory exits on the lock, before binding
// or writing any state (no token, project, or workstream files).
func TestServeLockConflictMutatesNothing(t *testing.T) {
	o := persistentOptions(t, freeAddr(t))
	release, err := acquireStateLock(StateLockFile())
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	defer release()
	err = Serve(o)
	if err == nil || !strings.Contains(err.Error(), "another ycc daemon") {
		t.Fatalf("Serve = %v, want lock conflict", err)
	}
	if got := stateEntries(t); len(got) != 1 || got[0] != "daemon.lock" {
		t.Fatalf("state dir = %v, want only daemon.lock", got)
	}
	if ln, err := net.Listen("tcp", o.Addr); err != nil {
		t.Fatalf("address left bound after lock conflict: %v", err)
	} else {
		ln.Close()
	}
}

// A busy port fails the bind before any state is written, and releases the lock.
func TestServeBindFailureMutatesNothing(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	o := persistentOptions(t, busy.Addr().String())
	if err := Serve(o); err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("Serve = %v, want listen failure", err)
	}
	if got := stateEntries(t); len(got) != 1 || got[0] != "daemon.lock" {
		t.Fatalf("state dir = %v, want only daemon.lock", got)
	}
	release, err := acquireStateLock(StateLockFile())
	if err != nil {
		t.Fatalf("lock still held after bind failure: %v", err)
	}
	release()
}

// SIGTERM shuts a persistent daemon down gracefully: Serve returns nil, the
// listener closes, and the state lock is released for the next daemon.
func TestServeGracefulShutdownOnSignal(t *testing.T) {
	o := persistentOptions(t, freeAddr(t))
	done := make(chan error, 1)
	go func() { done <- Serve(o) }()
	base := "http://" + o.Addr
	deadline := time.Now().Add(10 * time.Second)
	for {
		token := ReadLocalToken()
		if token != "" && Reachable(base, token) {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Serve exited early: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon never became reachable")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Admission is open once startup finished.
	client := DialClient(base, ReadLocalToken())
	if _, err := client.ListProjects(context.Background(), connect.NewRequest(&v1.ListProjectsRequest{})); err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve after SIGTERM = %v, want nil", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Serve did not return after SIGTERM")
	}
	if Reachable(base, ReadLocalToken()) {
		t.Fatal("daemon still reachable after shutdown")
	}
	release, err := acquireStateLock(StateLockFile())
	if err != nil {
		t.Fatalf("state lock not released after shutdown: %v", err)
	}
	release()
}

// One deadline from the signal bounds the fence and the drain together: an RPC
// that never returns cannot extend how long sessions keep running.
func TestGracefulShutdownDeadlineBoundsFence(t *testing.T) {
	o := persistentOptions(t, "127.0.0.1:0")
	o.Persist = false
	p, err := buildHandler(o, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := p.mgr.Start(session.Config{Workspace: o.Workspace, Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	a := server.NewAdmission()
	a.Open()
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	h := a.Handler("/rpc/", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-release
	}))
	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/rpc/SendInput", nil))
	<-entered
	done := make(chan struct{})
	go func() {
		gracefulShutdown(100*time.Millisecond, make(chan os.Signal), a, p.mgr, func() {})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("graceful shutdown outlived its 100ms deadline by seconds")
	}
	if ev := s.Log().Record("probe", event.SessionNotice, nil); ev.Seq != 0 {
		t.Fatal("session log still accepts writes after shutdown")
	}
}
