package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

// TestCompletionScripts runs the built-in completion command in-process for each
// supported shell and asserts a non-empty script mentioning the app name is
// emitted to the command's Writer.
func TestCompletionScripts(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			var out bytes.Buffer
			root := newRootCommand(&app{})
			root.Writer = &out
			if err := root.Run(context.Background(), []string{"ycc", "completion", shell}); err != nil {
				t.Fatalf("completion %s: %v", shell, err)
			}
			script := out.String()
			if strings.TrimSpace(script) == "" {
				t.Fatalf("completion %s emitted an empty script", shell)
			}
			if !strings.Contains(script, "ycc") {
				t.Fatalf("completion %s script does not mention ycc:\n%s", shell, script)
			}
		})
	}
}

// TestCompletionClientDaemonOptional exercises both branches of
// completionClient: an explicit --addr always yields a client, while no --addr
// and no reachable local daemon yields nil (so completers stay silent and never
// spin up the one-shot in-process daemon).
func TestCompletionClientDaemonOptional(t *testing.T) {
	// An explicit --addr always yields a client (the daemon may be remote and we
	// don't probe it here).
	a := &app{addr: "http://127.0.0.1:1"}
	if a.completionClient() == nil {
		t.Fatal("completionClient() with --addr returned nil")
	}

	// No credentials: discovery must neither dial a tokenless daemon nor create
	// state, irrespective of whether a daemon is running on the development host.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if c := (&app{}).completionClient(); c != nil {
		t.Fatal("completionClient() with no --addr and no daemon should be nil")
	}
}

// TestCompleteSessionIDsFlagFallback: when the word being completed is a flag,
// completeSessionIDs must delegate to the default flag completer (emitting flag
// suggestions) rather than dialing a daemon for session ids.
func TestCompleteSessionIDsFlagFallback(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	saved := os.Args
	args := []string{"ycc", "attach", "--f", "--generate-shell-completion"}
	os.Args = args
	t.Cleanup(func() { os.Args = saved })

	var out bytes.Buffer
	root := newRootCommand(&app{})
	root.Writer = &out
	// Run through the full completion machinery; completing "--f" should surface
	// the attach --from flag rather than dialing a daemon for session ids.
	if err := root.Run(context.Background(), args); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out.String(), "from") {
		t.Fatalf("flag-prefixed completion did not emit flag suggestions:\n%s", out.String())
	}
}
