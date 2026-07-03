package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestMain dispatches the sandbox helper when this test binary is re-executed
// with HelperArg (so os.Executable() → the test binary works for the Landlock
// re-exec path). Otherwise it runs the normal test suite.
func TestMain(m *testing.M) {
	MaybeHelper() // returns immediately unless we are the helper
	os.Exit(m.Run())
}

// workspace returns a fresh workspace root plus a scratch dir that sits OUTSIDE
// (a sibling of) the workspace, and points TMPDIR at the scratch dir. This
// mirrors a realistic layout where the workspace is not nested under the system
// temp dir, so os.TempDir() (on the write allowlist) does not overlap the
// workspace. Without this, t.TempDir() would place the workspace under /tmp and
// the allowlist would (correctly) exclude all of /tmp, leaving nowhere writable.
func workspace(t *testing.T) (root, scratch string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "ws")
	scratch = filepath.Join(base, "scratch")
	for _, d := range []string{root, scratch} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TMPDIR", scratch)
	return root, scratch
}

// run executes script through sandbox.Command against root and returns combined
// output plus the run error. It propagates the current environment (including the
// TMPDIR set by workspace) to the sandbox helper.
func run(t *testing.T, root, script string) ([]byte, error) {
	t.Helper()
	cmd, _ := Command(context.Background(), root, script)
	cmd.Dir = root
	cmd.Env = os.Environ()
	return cmd.CombinedOutput()
}

func skipUnlessSandboxed(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("sandbox enforcement is Linux-only")
	}
	if Available() == None {
		t.Skip("no sandbox mechanism available on this host")
	}
}

func TestSandboxBlocksWorkspaceWrite(t *testing.T) {
	skipUnlessSandboxed(t)
	root, _ := workspace(t)
	target := filepath.Join(root, "x")
	if _, err := run(t, root, "touch "+target); err == nil {
		t.Fatalf("expected touch inside workspace to fail")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("workspace file was created despite sandbox: err=%v", err)
	}
}

func TestSandboxBlocksWorkspaceDelete(t *testing.T) {
	skipUnlessSandboxed(t)
	root, _ := workspace(t)
	victim := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(victim, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, root, "rm -f "+victim); err == nil {
		t.Fatalf("expected rm inside workspace to fail")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("workspace file was deleted despite sandbox: %v", err)
	}
}

func TestSandboxAllowsReadInspection(t *testing.T) {
	skipUnlessSandboxed(t)
	root, _ := workspace(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{
		"cat a.txt",
		"ls -a",
		"grep hello a.txt",
	} {
		out, err := run(t, root, script)
		if err != nil {
			t.Fatalf("read-only command %q failed: %v (%s)", script, err, out)
		}
	}
}

func TestSandboxAllowsGitDiff(t *testing.T) {
	skipUnlessSandboxed(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root, _ := workspace(t)
	// Build a real git repo with an uncommitted change. This mutates the
	// workspace, so it must run UNSANDBOXED (plain sh).
	setup := "git init -q && git config user.email t@t && git config user.name t && " +
		"printf 'one\\n' > f.txt && git add f.txt && git commit -qm init && printf 'two\\n' >> f.txt"
	c := exec.Command("sh", "-c", setup)
	c.Dir = root
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git setup failed: %v (%s)", err, out)
	}

	out, err := run(t, root, "git diff")
	if err != nil {
		t.Fatalf("sandboxed git diff failed: %v (%s)", err, out)
	}
	if len(out) == 0 {
		t.Fatalf("git diff produced no output")
	}
}

func TestSandboxAllowsTempWrite(t *testing.T) {
	skipUnlessSandboxed(t)
	root, scratch := workspace(t)
	// A scratch file under os.TempDir() (== scratch here, on the write allowlist
	// and outside the workspace) must be writable.
	target := filepath.Join(scratch, "out.txt")
	if out, err := run(t, root, "echo hi > "+target); err != nil {
		t.Fatalf("write to temp file failed: %v (%s)", err, out)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("temp file not created: %v", err)
	}
}

// TestLandlockBlocksSymlinkIntoWorkspace verifies the symlink-proof property:
// writing through a symlink in a writable dir that points into the read-only
// workspace is denied (Landlock resolves the real inode).
func TestLandlockBlocksSymlinkIntoWorkspace(t *testing.T) {
	skipUnlessSandboxed(t)
	if Available() != Landlock {
		t.Skip("symlink-proof write denial is a Landlock property")
	}
	root, scratch := workspace(t)
	link := filepath.Join(scratch, "link")
	if err := os.Symlink(filepath.Join(root, "escape.txt"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, root, "echo pwned > "+link); err == nil {
		t.Fatalf("expected write through symlink into workspace to fail")
	}
	if _, err := os.Stat(filepath.Join(root, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("workspace file created through symlink: err=%v", err)
	}
}

func TestShellSingleQuote(t *testing.T) {
	cases := map[string]string{
		"hello":       `'hello'`,
		"a b":         `'a b'`,
		"it's":        `'it'\''s'`,
		"'; rm -rf /": `''\''; rm -rf /'`,
	}
	for in, want := range cases {
		if got := shellSingleQuote(in); got != want {
			t.Errorf("shellSingleQuote(%q) = %q, want %q", in, got, want)
		}
	}
	// The quoted string must round-trip through sh: a value passed to echo comes
	// back verbatim, so no embedded content can break out of the quoting.
	for in := range cases {
		out, err := exec.Command("sh", "-c", "printf %s "+shellSingleQuote(in)).Output()
		if err != nil {
			t.Fatalf("sh printf %q: %v", in, err)
		}
		if string(out) != in {
			t.Errorf("round-trip: printf of %q produced %q", in, out)
		}
	}
}
