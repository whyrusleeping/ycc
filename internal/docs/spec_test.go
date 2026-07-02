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

// DocFiles enumerates the spec entry point plus every doc_globs match, deduped
// and sorted; it skips directories, .git, and non-matching files.
func TestDocFiles(t *testing.T) {
	ws := t.TempDir()
	writeYCCConfig(t, ws, "doc_globs = [\"docs/**\", \"ARCHITECTURE.md\"]\n")
	write := func(rel, body string) {
		abs := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("spec.md", "# spec\n")
	write("docs/design.md", "d\n")
	write("docs/api/rpc.md", "r\n")
	write("ARCHITECTURE.md", "a\n")
	write("README.md", "not a doc\n")
	write(".git/config", "x\n")

	s := NewStore(ws)
	files, err := s.DocFiles()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range files {
		rel, _ := filepath.Rel(ws, f)
		got[filepath.ToSlash(rel)] = true
	}
	for _, want := range []string{"spec.md", "docs/design.md", "docs/api/rpc.md", "ARCHITECTURE.md"} {
		if !got[want] {
			t.Fatalf("DocFiles() missing %q; got %v", want, files)
		}
	}
	for _, bad := range []string{"README.md", ".git/config"} {
		if got[bad] {
			t.Fatalf("DocFiles() should not include %q", bad)
		}
	}
	// Stable sorted order.
	for i := 1; i < len(files); i++ {
		if files[i-1] > files[i] {
			t.Fatalf("DocFiles() not sorted: %v", files)
		}
	}
}

// With no config the docs set is exactly the spec entry point (when present).
func TestDocFilesDefault(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "spec.md"), []byte("# spec\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := NewStore(ws).DocFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || filepath.Base(files[0]) != "spec.md" {
		t.Fatalf("DocFiles() = %v, want just spec.md", files)
	}

	// No spec.md and no globs → empty docs set.
	empty, err := NewStore(t.TempDir()).DocFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("DocFiles() on empty workspace = %v, want none", empty)
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
