package workstream

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSafeProjectDir(t *testing.T) {
	safe := []string{
		"demo",
		"Project_1",
		"a.b-c",
		"a..b",
		"9project",
	}
	for _, name := range safe {
		if got := SafeProjectDir(name); got != name {
			t.Errorf("SafeProjectDir(%q) = %q, want unchanged", name, got)
		}
	}

	unsafe := []string{
		"../../escape",
		"a/b",
		`a\b`,
		"/etc",
		`C:\etc`,
		"日本語",
		"two words",
		".",
		"..",
		"",
	}
	validComponent := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	seen := make(map[string]string)
	root := t.TempDir()
	for _, name := range unsafe {
		got := SafeProjectDir(name)
		if !validComponent.MatchString(got) {
			t.Errorf("SafeProjectDir(%q) = %q, not a safe component", name, got)
		}
		if strings.ContainsAny(got, `/\`) || filepath.Base(got) != got {
			t.Errorf("SafeProjectDir(%q) = %q, not a single component", name, got)
		}
		if previous, ok := seen[got]; ok {
			t.Errorf("SafeProjectDir collision: %q and %q both map to %q", previous, name, got)
		}
		seen[got] = name
		path, err := ContainedPath(root, got, "ws_test")
		if err != nil {
			t.Errorf("ContainedPath for SafeProjectDir(%q): %v", name, err)
			continue
		}
		if err := VerifyUnderRoot(root, path); err != nil {
			t.Errorf("generated path for %q is not contained: %v", name, err)
		}
	}

	// A sanitized spelling must not collapse onto the natural safe spelling.
	if got, safeGot := SafeProjectDir("a/b"), SafeProjectDir("a_b"); got == safeGot {
		t.Fatalf("unsafe collision pair aliases: a/b and a_b both map to %q", got)
	}
	// Generated names occupy a reserved suffix namespace. A project whose literal
	// display name equals another project's generated directory must be transformed
	// again rather than preserved and aliased.
	const generatedLooking = "a_b-c14cddc033f64b9d"
	if got := SafeProjectDir("a/b"); got != generatedLooking {
		t.Fatalf("SafeProjectDir(a/b) = %q, want %q", got, generatedLooking)
	}
	if got := SafeProjectDir(generatedLooking); got == generatedLooking {
		t.Fatalf("literal generated-looking name %q was preserved and aliases a/b", got)
	}
	if got := SafeProjectDir(generatedLooking); got == SafeProjectDir("a/b") {
		t.Fatalf("generated-output-vs-literal collision: both map to %q", got)
	}
	// The mapping is stable across calls.
	if a, b := SafeProjectDir("../../escape"), SafeProjectDir("../../escape"); a != b {
		t.Fatalf("mapping is not stable: %q != %q", a, b)
	}
}

func TestContainedPath(t *testing.T) {
	root := t.TempDir()
	got, err := ContainedPath(root, "project", "ws_1234")
	if err != nil {
		t.Fatalf("ContainedPath(valid): %v", err)
	}
	want := filepath.Join(root, "project", "ws_1234")
	if got != want {
		t.Fatalf("ContainedPath(valid) = %q, want %q", got, want)
	}

	for name, parts := range map[string][]string{
		"no parts":       nil,
		"root itself":    {"project", ".."},
		"parent":         {".."},
		"deep traversal": {"../../escape", "ws_1234"},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := ContainedPath(root, parts...); err == nil {
				t.Fatalf("ContainedPath(%q, %q) = %q, want error", root, parts, got)
			}
		})
	}
}

func TestVerifyUnderRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "worktrees")
	inside := filepath.Join(root, "project", "ws_1234")
	if err := VerifyUnderRoot(root, inside); err != nil {
		t.Fatalf("VerifyUnderRoot(inside): %v", err)
	}

	for name, path := range map[string]string{
		"equal root":     root,
		"parent":         parent,
		"sibling prefix": filepath.Join(parent, "worktrees-elsewhere", "ws_1234"),
		"relative":       filepath.Join("project", "ws_1234"),
		"empty":          "",
	} {
		t.Run(name, func(t *testing.T) {
			if err := VerifyUnderRoot(root, path); err == nil {
				t.Fatalf("VerifyUnderRoot(%q, %q) succeeded, want error", root, path)
			}
		})
	}
}
