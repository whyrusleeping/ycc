package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// workerRegRoots builds a worker registry whose workspace allows the given extra
// read-only roots.
func workerRegRoots(root string, readRoots ...string) *Registry {
	reg := New()
	reg.Add(Worker(&Workspace{Root: root, ReadRoots: readRoots})...)
	return reg
}

// (a) A file under a configured ReadRoot can be read and its contents returned.
func TestReadFromReadRoot(t *testing.T) {
	root := t.TempDir()
	extRoot := t.TempDir()
	target := filepath.Join(extRoot, "dep", "lib.go")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("package dep // hello-from-readroot"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := workerRegRoots(root, extRoot)

	res := dispatch(t, reg, "Read", `{"file_path":"`+target+`"}`)
	if res.IsError || !strings.Contains(res.Content, "hello-from-readroot") {
		t.Fatalf("Read from read root = %q (err=%v)", res.Content, res.IsError)
	}
}

// (b) A path outside the workspace and all ReadRoots fails with a clear error.
func TestReadOutsideAllRootsFails(t *testing.T) {
	root := t.TempDir()
	forbidden := t.TempDir()
	target := filepath.Join(forbidden, "secret.txt")
	if err := os.WriteFile(target, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No ReadRoots configured at all.
	reg := workerRegRoots(root)
	res := dispatch(t, reg, "Read", `{"file_path":"`+target+`"}`)
	if !res.IsError || !strings.Contains(res.Content, "not within a trusted read-only root") {
		t.Fatalf("expected outside-roots rejection, got %q (err=%v)", res.Content, res.IsError)
	}
}

// (c) Write and Edit targeting a path inside a ReadRoot are still rejected:
// writes remain confined to the workspace.
func TestWriteEditConfinedDespiteReadRoot(t *testing.T) {
	root := t.TempDir()
	extRoot := t.TempDir()
	existing := filepath.Join(extRoot, "lib.go")
	if err := os.WriteFile(existing, []byte("package lib"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := workerRegRoots(root, extRoot)

	// Write into the read root must be rejected as outside the workspace.
	wpath := filepath.Join(extRoot, "new.txt")
	res := dispatch(t, reg, "Write", `{"file_path":"`+wpath+`","content":"x"}`)
	if !res.IsError || !strings.Contains(res.Content, "outside the workspace") {
		t.Fatalf("expected Write rejection, got %q (err=%v)", res.Content, res.IsError)
	}
	if _, err := os.Stat(wpath); !os.IsNotExist(err) {
		t.Fatalf("Write should not have created file in read root: err=%v", err)
	}

	// Edit of an existing file in the read root must also be rejected.
	res = dispatch(t, reg, "Edit", `{"file_path":"`+existing+`","old_string":"lib","new_string":"pwned"}`)
	if !res.IsError || !strings.Contains(res.Content, "outside the workspace") {
		t.Fatalf("expected Edit rejection, got %q (err=%v)", res.Content, res.IsError)
	}
	if got, _ := os.ReadFile(existing); string(got) != "package lib" {
		t.Fatalf("Edit should not have modified file in read root, got %q", got)
	}
}

// (d) A symlink inside an allowed read root that points OUTSIDE the allowlist
// cannot be used to read the target: resolveRead is symlink-aware.
func TestReadRootSymlinkEscapeRejected(t *testing.T) {
	root := t.TempDir()
	extRoot := t.TempDir()
	forbidden := t.TempDir()
	secret := filepath.Join(forbidden, "secret.txt")
	if err := os.WriteFile(secret, []byte("top-secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink located inside the allowed root pointing at the forbidden dir.
	link := filepath.Join(extRoot, "escape")
	if err := os.Symlink(forbidden, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	reg := workerRegRoots(root, extRoot)

	res := dispatch(t, reg, "Read", `{"file_path":"`+filepath.Join(link, "secret.txt")+`"}`)
	if !res.IsError || !strings.Contains(res.Content, "not within a trusted read-only root") {
		t.Fatalf("expected symlink-escape rejection, got %q (err=%v)", res.Content, res.IsError)
	}
}

func TestDefaultReadRootsIncludesModCache(t *testing.T) {
	t.Setenv("GOMODCACHE", "/custom/modcache")
	roots := DefaultReadRoots()
	found := false
	for _, r := range roots {
		if r == filepath.Clean("/custom/modcache") {
			found = true
		}
	}
	if !found {
		t.Fatalf("DefaultReadRoots %v should include GOMODCACHE", roots)
	}
}

func TestGoModCacheFallbacks(t *testing.T) {
	// GOPATH fallback when GOMODCACHE unset.
	t.Setenv("GOMODCACHE", "")
	t.Setenv("GOPATH", "/my/gopath")
	if got := goModCache(); got != filepath.Clean("/my/gopath/pkg/mod") {
		t.Fatalf("GOPATH fallback = %q", got)
	}
}

func TestReadRootsDedupAndExtras(t *testing.T) {
	extra := []string{"", "/a", "/a", "/b"}
	out := ReadRoots(extra)
	seen := map[string]int{}
	for _, r := range out {
		seen[r]++
		if r == "" {
			t.Fatalf("ReadRoots should drop empty entries: %v", out)
		}
	}
	for r, n := range seen {
		if n > 1 {
			t.Fatalf("duplicate %q in %v", r, out)
		}
	}
	// extras present
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
		t.Fatalf("extras missing from %v", out)
	}
}
