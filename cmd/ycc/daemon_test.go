package main

import (
	"strconv"
	"testing"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/daemon"
)

func TestDaemonCommandBuiltinFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{name: "no model flags", want: true},
		{name: "explicit model", args: []string{"--model", "claude-opus-5-5"}},
		{name: "explicit base URL", args: []string{"--base-url", config.DefaultAnthropicBaseURL}},
		{name: "explicit key env", args: []string{"--key-env", "ANTHROPIC_API_KEY"}},
		{name: "explicit max tokens", args: []string{"--max-tokens", strconv.Itoa(config.DefaultMaxTokens)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("APPDATA", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			// Each explicit flag supplies its default value: presence, not a
			// difference from the default, must distinguish a chosen config.
			called := false
			cmd := daemonCommand(func(o daemon.Options) error {
				called = true
				if o.ConfigPath != "" || o.BuiltinFallback != tc.want {
					t.Errorf("config path = %q, built-in fallback = %v; want no config path and fallback = %v", o.ConfigPath, o.BuiltinFallback, tc.want)
				}
				return nil
			})
			if err := cmd.Run(t.Context(), append([]string{"daemon", "--workspace", t.TempDir()}, tc.args...)); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("daemon command did not pass its parsed options to startup")
			}
		})
	}
}
