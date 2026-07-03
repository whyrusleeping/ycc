package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/whyrusleeping/ycc/internal/sandbox"
)

// TestMain dispatches the sandbox helper when this test binary is re-executed
// with sandbox.HelperArg (the reviewer Bash landlock path re-execs os.Executable,
// which is this test binary). Otherwise it runs the suite normally.
func TestMain(m *testing.M) {
	sandbox.MaybeHelper()
	os.Exit(m.Run())
}

func reviewerReg(root string) *Registry {
	reg := New()
	reg.Add(Reviewer(&Workspace{Root: root})...)
	return reg
}

// TestReviewerBashCannotWriteWorkspace verifies the reviewer Bash tool cannot
// create a file in the workspace while the worker Bash tool still can. Skipped
// when no sandbox mechanism is available (prompt-only enforcement).
func TestReviewerBashCannotWriteWorkspace(t *testing.T) {
	if runtime.GOOS != "linux" || sandbox.Available() == sandbox.None {
		t.Skip("no sandbox mechanism available; reviewer non-mutation is prompt-only")
	}
	// Keep the workspace out of the temp dir so the write allowlist (which
	// includes os.TempDir()) does not overlap it.
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	scratch := filepath.Join(base, "scratch")
	for _, d := range []string{root, scratch} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TMPDIR", scratch)

	// Worker Bash can write.
	wreg := workerReg(root)
	if res := dispatch(t, wreg, "Bash", `{"command":"touch worker.txt"}`); res.IsError {
		t.Fatalf("worker bash: %s", res.Content)
	}
	if _, err := os.Stat(filepath.Join(root, "worker.txt")); err != nil {
		t.Fatalf("worker bash should have created the file: %v", err)
	}

	// Reviewer Bash cannot write to the workspace.
	rreg := reviewerReg(root)
	dispatch(t, rreg, "Bash", `{"command":"touch reviewer.txt"}`)
	if _, err := os.Stat(filepath.Join(root, "reviewer.txt")); !os.IsNotExist(err) {
		t.Fatalf("reviewer bash created a file in the workspace despite the sandbox: err=%v", err)
	}

	// Read-only inspection through reviewer Bash still works.
	if res := dispatch(t, rreg, "Bash", `{"command":"ls"}`); res.IsError {
		t.Fatalf("reviewer bash ls failed: %s", res.Content)
	}
}

// TestResolveSymlinkEscape verifies Write/Edit reject a path that resolves out of
// the workspace through an in-workspace symlink, while normal in-workspace paths
// still work. Runs on all platforms (no sandbox needed).
func TestResolveSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	// A symlinked directory inside the workspace pointing outside it.
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	reg := workerReg(root)

	// Write through the symlink must be rejected.
	res := dispatch(t, reg, "Write", `{"file_path":"escape/pwned.txt","content":"x"}`)
	if !res.IsError {
		t.Fatalf("Write through symlink escape should be rejected, got: %s", res.Content)
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned.txt")); !os.IsNotExist(err) {
		t.Fatalf("file was written outside the workspace via symlink: err=%v", err)
	}

	// A normal relative in-workspace path still works.
	if res := dispatch(t, reg, "Write", `{"file_path":"ok.txt","content":"hi"}`); res.IsError {
		t.Fatalf("normal Write should succeed: %s", res.Content)
	}
	// An absolute in-workspace path still works.
	abs := filepath.Join(root, "ok2.txt")
	if res := dispatch(t, reg, "Write", `{"file_path":"`+abs+`","content":"hi"}`); res.IsError {
		t.Fatalf("absolute in-workspace Write should succeed: %s", res.Content)
	}

	// Edit through the symlink must also be rejected (create a target first).
	if err := os.WriteFile(filepath.Join(outside, "t.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	res = dispatch(t, reg, "Edit", `{"file_path":"escape/t.txt","old_string":"a","new_string":"b"}`)
	if !res.IsError {
		t.Fatalf("Edit through symlink escape should be rejected, got: %s", res.Content)
	}
	if got, _ := os.ReadFile(filepath.Join(outside, "t.txt")); string(got) != "a" {
		t.Fatalf("Edit modified a file outside the workspace via symlink: %q", got)
	}
}
