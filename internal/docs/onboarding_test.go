package docs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNeedsOnboarding(t *testing.T) {
	t.Run("empty workspace", func(t *testing.T) {
		if !NeedsOnboarding(t.TempDir()) {
			t.Fatal("empty workspace should need onboarding")
		}
	})

	t.Run("substantive spec", func(t *testing.T) {
		workspace := t.TempDir()
		if err := os.WriteFile(filepath.Join(workspace, "spec.md"), []byte("# Spec\n\nShip it.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if NeedsOnboarding(workspace) {
			t.Fatal("workspace with a substantive spec should not need onboarding")
		}
	})

	t.Run("backlog task", func(t *testing.T) {
		workspace := t.TempDir()
		if err := os.Mkdir(filepath.Join(workspace, "backlog"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace, "backlog", "0001-first.md"), []byte("---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if NeedsOnboarding(workspace) {
			t.Fatal("workspace with a backlog task should not need onboarding")
		}
	})

	t.Run("configured spec entry point", func(t *testing.T) {
		workspace := t.TempDir()
		if err := os.MkdirAll(filepath.Join(workspace, ".ycc"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace, ".ycc", "config.toml"), []byte("spec_path = \"docs/index.md\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(workspace, "docs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace, "docs", "index.md"), []byte("# Design\n\nReal content.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if NeedsOnboarding(workspace) {
			t.Fatal("configured substantive spec should count as onboarded")
		}
	})
}
