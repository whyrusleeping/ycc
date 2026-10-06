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
	}{
		{"loopback", "127.0.0.1:0", "", false, true},
		{"loopback web", "127.0.0.1:0", "", true, true},
		{"explicit", "127.0.0.1:0", "configured", false, false},
		{"non-loopback", "0.0.0.0:0", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			t.Setenv("YCC_TOKEN", tc.token)
			// Config failure ends the command before it listens or mutates project
			// state, but after local credentials are resolved.
			args := []string{"ycc", "daemon", "--addr", tc.addr, "--config", filepath.Join(t.TempDir(), "missing.toml")}
			if tc.web {
				args = append(args, "--web")
			}
			err := newRootCommand(&app{}).Run(context.Background(), args)
			if err == nil || !strings.Contains(err.Error(), "load config") {
				t.Fatalf("expected config error after credential resolution: %v", err)
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
	addr, token, persistent, shutdown, err := resolveDaemon("", "", false, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer shutdown()
	if token == "" || persistent || !daemon.Reachable(addr, token) {
		t.Fatal("in-process resolution did not return usable credentials")
	}
	if daemon.Reachable(addr, "") {
		t.Fatal("in-process daemon accepted a tokenless client")
	}
	if daemon.ReadLocalToken() != "" {
		t.Fatal("one-shot daemon persisted its token")
	}
}
