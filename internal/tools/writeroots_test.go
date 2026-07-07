package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// workerRegWrite builds a worker registry whose workspace allows the given
// extra writable roots.
func workerRegWrite(root string, writeRoots ...string) *Registry {
	reg := New()
	reg.Add(Worker(&Workspace{Root: root, WriteRoots: writeRoots})...)
	return reg
}

// (a) Reads are unrestricted: a file anywhere on disk can be read.
func TestReadOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	target := filepath.Join(elsewhere, "sibling", "lib.go")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("package sibling // hello-from-outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := workerRegWrite(root)

	res := dispatch(t, reg, "Read", `{"file_path":"`+target+`"}`)
	if res.IsError || !strings.Contains(res.Content, "hello-from-outside") {
		t.Fatalf("Read outside workspace = %q (err=%v)", res.Content, res.IsError)
	}

	// Relative ../ paths resolve against the root and are also readable.
	res = dispatch(t, reg, "Read", `{"file_path":"../"}`)
	if res.IsError {
		t.Fatalf("Read ../ = %q (err=%v)", res.Content, res.IsError)
	}
}

// (b) Write and Edit outside the workspace are rejected when no write root
// covers the path: writes stay confined by default.
func TestWriteEditConfinedByDefault(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	existing := filepath.Join(outside, "lib.go")
	if err := os.WriteFile(existing, []byte("package lib"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := workerRegWrite(root)

	wpath := filepath.Join(outside, "new.txt")
	res := dispatch(t, reg, "Write", `{"file_path":"`+wpath+`","content":"x"}`)
	if !res.IsError || !strings.Contains(res.Content, "outside the workspace") {
		t.Fatalf("expected Write rejection, got %q (err=%v)", res.Content, res.IsError)
	}
	if _, err := os.Stat(wpath); !os.IsNotExist(err) {
		t.Fatalf("Write should not have created file outside the workspace: err=%v", err)
	}

	res = dispatch(t, reg, "Edit", `{"file_path":"`+existing+`","old_string":"lib","new_string":"pwned"}`)
	if !res.IsError || !strings.Contains(res.Content, "outside the workspace") {
		t.Fatalf("expected Edit rejection, got %q (err=%v)", res.Content, res.IsError)
	}
	if got, _ := os.ReadFile(existing); string(got) != "package lib" {
		t.Fatalf("Edit should not have modified file outside the workspace, got %q", got)
	}
}

// (c) Write and Edit within a configured write root succeed.
func TestWriteEditWithinWriteRoot(t *testing.T) {
	root := t.TempDir()
	sibling := t.TempDir()
	existing := filepath.Join(sibling, "lib.go")
	if err := os.WriteFile(existing, []byte("package lib"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := workerRegWrite(root, sibling)

	wpath := filepath.Join(sibling, "sub", "new.txt")
	res := dispatch(t, reg, "Write", `{"file_path":"`+wpath+`","content":"hello"}`)
	if res.IsError {
		t.Fatalf("Write into write root failed: %q", res.Content)
	}
	if got, _ := os.ReadFile(wpath); string(got) != "hello" {
		t.Fatalf("Write into write root wrote %q", got)
	}

	res = dispatch(t, reg, "Edit", `{"file_path":"`+existing+`","old_string":"lib","new_string":"lib2"}`)
	if res.IsError {
		t.Fatalf("Edit in write root failed: %q", res.Content)
	}
	if got, _ := os.ReadFile(existing); string(got) != "package lib2" {
		t.Fatalf("Edit in write root produced %q", got)
	}
}

// (d) A symlink inside a write root that points OUTSIDE all writable roots
// cannot be used to write to the target: resolve is symlink-aware.
func TestWriteRootSymlinkEscapeRejected(t *testing.T) {
	root := t.TempDir()
	sibling := t.TempDir()
	forbidden := t.TempDir()
	victim := filepath.Join(forbidden, "victim.txt")
	if err := os.WriteFile(victim, []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(sibling, "escape")
	if err := os.Symlink(forbidden, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	reg := workerRegWrite(root, sibling)

	res := dispatch(t, reg, "Write", `{"file_path":"`+filepath.Join(link, "victim.txt")+`","content":"pwned"}`)
	if !res.IsError {
		t.Fatalf("expected symlink-escape rejection, got %q", res.Content)
	}
	if got, _ := os.ReadFile(victim); string(got) != "untouched" {
		t.Fatalf("Write escaped write root via symlink: %q", got)
	}
}

func TestNormalizeRoots(t *testing.T) {
	out := NormalizeRoots([]string{"", "/a", "/a/", "/b", "  "})
	seen := map[string]int{}
	for _, r := range out {
		if strings.TrimSpace(r) == "" {
			t.Fatalf("NormalizeRoots kept an empty entry: %v", out)
		}
		seen[r]++
	}
	for r, n := range seen {
		if n > 1 {
			t.Fatalf("duplicate %q in %v", r, out)
		}
	}
	var hasA, hasB bool
	for _, r := range out {
		if r == filepath.Clean("/a") {
			hasA = true
		}
		if r == filepath.Clean("/b") {
			hasB = true
		}
	}
	if !hasA || !hasB {
		t.Fatalf("entries missing from %v", out)
	}
}
