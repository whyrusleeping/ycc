package workstream

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
)

func TestBootstrapCopyLinkAndSetup(t *testing.T) {
	primary := t.TempDir()
	dir := t.TempDir()

	copySource := filepath.Join(primary, ".env")
	if err := os.WriteFile(copySource, []byte("SECRET=value\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(copySource, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(primary, "local", "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(primary, "local", "nested", "value.txt"), []byte("nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(primary, "node_modules")
	if err := os.Mkdir(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("worktree"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(primary, "keep.txt"), []byte("primary"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Worktree{
		Copy:  []string{".env", "missing.env", "keep.txt", "local"},
		Link:  []string{"node_modules", "missing-deps"},
		Setup: []string{`printf '%s' "$BOOTSTRAP_VALUE" > setup.txt`},
		Env:   map[string]string{"BOOTSTRAP_VALUE": "from-env"},
	}
	if err := Bootstrap(primary, dir, cfg); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil || string(data) != "SECRET=value\n" {
		t.Fatalf("copied .env = %q, err=%v", data, err)
	}
	if info, err := os.Stat(filepath.Join(dir, ".env")); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("copied mode = %v, err=%v", info, err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "local", "nested", "value.txt")); err != nil || string(data) != "nested" {
		t.Fatalf("recursive copy = %q, err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "missing.env")); !os.IsNotExist(err) {
		t.Fatalf("missing copy source was not skipped: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "keep.txt")); err != nil || string(data) != "worktree" {
		t.Fatalf("existing destination was clobbered: %q, err=%v", data, err)
	}
	link, err := os.Readlink(filepath.Join(dir, "node_modules"))
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	wantLink, _ := filepath.Abs(shared)
	if link != wantLink {
		t.Fatalf("link target = %q, want %q", link, wantLink)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "setup.txt")); err != nil || string(data) != "from-env" {
		t.Fatalf("setup output = %q, err=%v", data, err)
	}
}

func TestBootstrapRejectsEscapingPaths(t *testing.T) {
	primary := t.TempDir()
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		cfg  config.Worktree
	}{
		{name: "copy traversal", cfg: config.Worktree{Copy: []string{"../secret"}}},
		{name: "copy absolute", cfg: config.Worktree{Copy: []string{filepath.Join(string(filepath.Separator), "secret")}}},
		{name: "copy empty", cfg: config.Worktree{Copy: []string{""}}},
		{name: "link traversal", cfg: config.Worktree{Link: []string{"a/../../secret"}}},
		{name: "link absolute", cfg: config.Worktree{Link: []string{filepath.Join(string(filepath.Separator), "deps")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Bootstrap(primary, dir, tc.cfg)
			if err == nil || !strings.Contains(err.Error(), "does not escape") {
				t.Fatalf("Bootstrap error = %v", err)
			}
		})
	}
}

func TestBootstrapSetupFailureIncludesOutput(t *testing.T) {
	err := Bootstrap(t.TempDir(), t.TempDir(), config.Worktree{
		Setup: []string{`echo "dependency install exploded" >&2; exit 7`},
	})
	if err == nil {
		t.Fatal("expected setup failure")
	}
	if text := err.Error(); !strings.Contains(text, "dependency install exploded") || !strings.Contains(text, "exit 7") {
		t.Fatalf("setup error did not include command output and status: %v", err)
	}
}

func TestBootstrapSetupTimeoutKillsCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group setup execution is Unix-specific")
	}
	start := time.Now()
	err := Bootstrap(t.TempDir(), t.TempDir(), config.Worktree{
		Setup:               []string{"sleep 5"},
		SetupTimeoutSeconds: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "timed out after 1s") {
		t.Fatalf("timeout error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("timed-out setup took too long: %s", elapsed)
	}
}
