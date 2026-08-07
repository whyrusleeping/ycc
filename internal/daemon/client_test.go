package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDaemonLogPrivateModesAndRepair(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not provide Unix permission semantics")
	}
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", root)
	t.Setenv("HOME", root)
	path := daemonLogPath()
	f, err := openDaemonLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"daemon log directory": filepath.Dir(path), "daemon log": path} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm() & 0o077; got != 0 {
			t.Errorf("%s mode %04o has group/other permissions", name, info.Mode().Perm())
		}
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err = openDaemonLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm() & 0o077; got != 0 {
		t.Errorf("existing daemon log mode %04o was not repaired", info.Mode().Perm())
	}
}

func TestBackgroundDaemonCmdlineKeepsTokenOutOfArgv(t *testing.T) {
	const fakeToken = "fake-test-token"

	for _, tc := range []struct {
		name  string
		token string
	}{
		{name: "with token", token: fakeToken},
		{name: "without token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args, _ := backgroundDaemonCmdline("/tmp/workspace", "/tmp/ycc.toml", tc.token, []string{"PATH=/bin"})
			for _, arg := range args {
				if arg == "-token" || arg == "--token" || strings.HasPrefix(arg, "-token=") || strings.HasPrefix(arg, "--token=") {
					t.Fatal("background daemon argv contains a token flag")
				}
				if tc.token != "" && strings.Contains(arg, tc.token) {
					t.Fatal("background daemon argv contains the token value")
				}
			}
		})
	}
}

func TestBackgroundDaemonCmdlineReplacesTokenEnvironment(t *testing.T) {
	const fakeToken = "fake-test-token"
	baseEnv := []string{
		"PATH=/bin",
		"YCC_TOKEN=stale-one",
		"HOME=/tmp/home",
		"YCC_TOKEN=stale-two",
	}

	_, env := backgroundDaemonCmdline("", "", fakeToken, baseEnv)
	var tokenEntries int
	for _, entry := range env {
		if strings.HasPrefix(entry, "YCC_TOKEN=") {
			tokenEntries++
			if entry != "YCC_TOKEN="+fakeToken {
				t.Fatal("background daemon environment retained a stale token")
			}
		}
	}
	if tokenEntries != 1 {
		t.Fatalf("background daemon environment has %d YCC_TOKEN entries, want 1", tokenEntries)
	}
}

func TestBackgroundDaemonCmdlineStripsTokenEnvironmentWhenEmpty(t *testing.T) {
	baseEnv := []string{
		"PATH=/bin",
		"YCC_TOKEN=stale-one",
		"HOME=/tmp/home",
		"YCC_TOKEN=stale-two",
	}

	_, env := backgroundDaemonCmdline("", "", "", baseEnv)
	for _, entry := range env {
		if strings.HasPrefix(entry, "YCC_TOKEN=") {
			t.Fatal("background daemon environment contains YCC_TOKEN for an empty caller token")
		}
	}
}
