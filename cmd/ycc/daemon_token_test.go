package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/daemon"
)

func TestDaemonCommandLocalToken(t *testing.T) {
	for _, tc := range []struct {
		name, addr, token string
		web               bool
		wantFile          bool
		wantErr           string
	}{
		{"loopback", "127.0.0.1:0", "", false, true, "load config"},
		{"loopback web", "127.0.0.1:0", "", true, true, "load config"},
		{"explicit", "127.0.0.1:0", "configured", false, false, "load config"},
		// Refused before the daemon locks, binds, or resolves credentials.
		{"non-loopback", "0.0.0.0:0", "", false, false, "without a token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			t.Setenv("YCC_TOKEN", tc.token)
			// Config failure ends the command before it mutates project state, but
			// after it owns the state directory and listener and has resolved local
			// credentials.
			args := []string{"ycc", "daemon", "--addr", tc.addr, "--config", filepath.Join(t.TempDir(), "missing.toml")}
			if tc.web {
				args = append(args, "--web")
			}
			err := newRootCommand(&app{}).Run(context.Background(), args)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected %q error: %v", tc.wantErr, err)
			}
			if got := daemon.ReadLocalToken() != ""; got != tc.wantFile {
				t.Fatalf("local token created = %v, want %v", got, tc.wantFile)
			}
		})
	}
}

func TestResolveDaemonPassesInProcessToken(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	addr, token, shutdown, err := resolveDaemon("", "", t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer shutdown()
	if token == "" || addr == daemon.LocalAddr || !daemon.Reachable(addr, token) {
		t.Fatal("in-process resolution did not return usable credentials")
	}
	if daemon.Reachable(addr, "") {
		t.Fatal("in-process daemon accepted a tokenless client")
	}
	if daemon.ReadLocalToken() != "" {
		t.Fatal("one-shot daemon persisted its token")
	}
}
