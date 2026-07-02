package docs

import (
	"os"
	"path/filepath"
	"testing"
)

func writeYCCConfig(t *testing.T, ws, body string) {
	t.Helper()
	dir := filepath.Join(ws, ".ycc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Without a config, the spec entry point defaults to spec.md at the workspace root.
func TestSpecPathDefault(t *testing.T) {
	ws := t.TempDir()
	s := NewStore(ws)
	if got, want := s.SpecPath(), filepath.Join(ws, "spec.md"); got != want {
		t.Fatalf("SpecPath() = %q, want %q", got, want)
	}
}

// A configured spec_path is honored (relative to the workspace root).
func TestSpecPathConfigured(t *testing.T) {
	ws := t.TempDir()
	writeYCCConfig(t, ws, "spec_path = \"docs/index.md\"\n")
	s := NewStore(ws)
	if got, want := s.SpecPath(), filepath.Join(ws, "docs", "index.md"); got != want {
		t.Fatalf("SpecPath() = %q, want %q", got, want)
	}
}

// A spec_path that escapes the workspace (absolute or ".." ) is ignored and
// falls back to the default.
func TestSpecPathEscapeIgnored(t *testing.T) {
	for _, bad := range []string{"/etc/passwd", "../../outside.md", "../sibling/spec.md"} {
		ws := t.TempDir()
		writeYCCConfig(t, ws, "spec_path = \""+bad+"\"\n")
		s := NewStore(ws)
		if got, want := s.SpecPath(), filepath.Join(ws, "spec.md"); got != want {
			t.Fatalf("escaping spec_path %q: SpecPath() = %q, want default %q", bad, got, want)
		}
	}
}

// A missing or garbage config.toml falls back cleanly to defaults.
func TestSpecConfigMissingOrGarbage(t *testing.T) {
	ws := t.TempDir()
	// No config at all.
	if got := NewStore(ws).SpecPath(); got != filepath.Join(ws, "spec.md") {
		t.Fatalf("missing config: SpecPath() = %q", got)
	}
	// Garbage config.
	writeYCCConfig(t, ws, "this is not : valid = toml [[[\n")
	s := NewStore(ws)
	if got := s.SpecPath(); got != filepath.Join(ws, "spec.md") {
		t.Fatalf("garbage config: SpecPath() = %q", got)
	}
	if s.IsDoc(filepath.Join(ws, "docs", "a.md")) {
		t.Fatal("garbage config should yield no doc globs")
	}
}

// IsDoc matches the entry point and configured globs (including ** across
// directories), and rejects everything else / paths outside the workspace.
func TestIsDoc(t *testing.T) {
	ws := t.TempDir()
	writeYCCConfig(t, ws, "spec_path = \"docs/index.md\"\ndoc_globs = [\"docs/**\", \"ARCHITECTURE.md\", \"adr/*.md\"]\n")
	s := NewStore(ws)

	abs := func(p string) string { return filepath.Join(ws, filepath.FromSlash(p)) }

	match := []string{
		"docs/index.md",   // the entry point
		"docs/design.md",  // docs/** shallow
		"docs/api/rpc.md", // docs/** across directories
		"ARCHITECTURE.md", // literal
		"adr/0001-foo.md", // adr/*.md single segment
	}
	for _, p := range match {
		if !s.IsDoc(abs(p)) {
			t.Fatalf("IsDoc(%q) = false, want true", p)
		}
	}

	noMatch := []string{
		"README.md",          // not in the docs set
		"adr/nested/deep.md", // * does not cross a separator
		"src/main.go",        // unrelated
	}
	for _, p := range noMatch {
		if s.IsDoc(abs(p)) {
			t.Fatalf("IsDoc(%q) = true, want false", p)
		}
	}

	// A path outside the workspace never matches.
	if s.IsDoc(filepath.Join(filepath.Dir(ws), "elsewhere", "spec.md")) {
		t.Fatal("IsDoc matched a path outside the workspace")
	}
}

// The default (no config) docs set is exactly the entry point.
func TestIsDocDefault(t *testing.T) {
	ws := t.TempDir()
	s := NewStore(ws)
	if !s.IsDoc(filepath.Join(ws, "spec.md")) {
		t.Fatal("default IsDoc should match spec.md")
	}
	if s.IsDoc(filepath.Join(ws, "docs", "x.md")) {
		t.Fatal("default IsDoc should not match arbitrary docs (no globs configured)")
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"docs/**", "docs/a.md", true},
		{"docs/**", "docs/a/b/c.md", true},
		{"docs/**", "docs", false},
		{"**/x.md", "x.md", true},
		{"**/x.md", "a/b/x.md", true},
		{"*.md", "a.md", true},
		{"*.md", "a/b.md", false},
		{"adr/*.md", "adr/1.md", true},
		{"adr/*.md", "adr/sub/1.md", false},
		{"a?c", "abc", true},
		{"a?c", "a/c", false},
		{"literal.md", "literal.md", true},
		{"literal.md", "other.md", false},
	}
	for _, c := range cases {
		if got := matchGlob(c.pattern, c.name); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}
