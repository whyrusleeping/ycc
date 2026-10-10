package docs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsDocsLayer(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".ycc"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "spec_path = \"design/SPEC.txt\"\ndoc_globs = [\"adr/**\"]\n"
	if err := os.WriteFile(filepath.Join(ws, ".ycc", "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(ws)
	for rel, want := range map[string]bool{
		"backlog/0001-x.md":    true,
		"backlog/.ids":         true,
		"memory.md":            true,
		"plans/runbook.md":     true,
		"design/SPEC.txt":      true,
		"adr/0001.txt":         true,
		"docs/validation.md":   true,
		"README.markdown":      true,
		"main.go":              false,
		"internal/x/y.go":      false,
		"web/dist/index.html":  false,
		".git/COMMIT_EDITMSG":  false,
		".git/info/notes.md":   false,
		"design/other.txt":     false,
		"../outside/README.md": false,
	} {
		if got := s.IsDocsLayer(filepath.Join(ws, rel)); got != want {
			t.Errorf("IsDocsLayer(%s) = %v, want %v", rel, got, want)
		}
	}

	unlock, ok := s.DocsWriteLock(filepath.Join(ws, "main.go"))
	if ok || unlock != nil {
		t.Fatal("code path took the docs write lock")
	}
	unlock, ok = s.DocsWriteLock(filepath.Join(ws, "backlog", "0001-x.md"))
	if !ok {
		t.Fatal("task file not docs layer")
	}
	if s.mu.TryLock() {
		t.Fatal("docs write lock does not hold the store mutex")
	}
	unlock()
	if !s.mu.TryLock() {
		t.Fatal("unlock did not release the store mutex")
	}
	s.mu.Unlock()
}
