package daemon

import (
	"strings"
	"testing"
)

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
