package e2e

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/charmbracelet/x/vt"

	"github.com/whyrusleeping/ycc/internal/tui/snapshot"
)

// yccBin is the path to the ycc binary built once by TestMain.
var yccBin string

// TestMain builds the ycc binary a single time for the whole package, so every
// scenario drives the identical real artifact. The suite is skipped under
// `go test -short` (it spawns a subprocess under a PTY and is slower than a unit
// test) and when a PTY cannot be allocated (some sandboxes).
func TestMain(m *testing.M) {
	// testing.Short() reads a flag, so flags must be parsed before we consult it.
	flag.Parse()
	if testing.Short() {
		// launch() also skips each test under -short; returning early here avoids
		// paying the binary build cost at all.
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "ycc-e2e-bin")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: mkdir temp: %v\n", err)
		os.Exit(1)
	}
	bin := filepath.Join(dir, "ycc")
	build := exec.Command("go", "build", "-o", bin, "./cmd/ycc")
	build.Dir = repoRoot()
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: build ycc failed: %v\n%s\n", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	yccBin = bin
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// repoRoot returns the module root (two levels up from internal/e2e).
func repoRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

// harness owns a running ycc process attached to a PTY plus the VT emulator its
// output is streamed into. Tests interact through send* (keystrokes) and waitFor
// (screen predicates).
type harness struct {
	t          *testing.T
	stub       *llmStub
	cmd        *exec.Cmd
	ptmx       *os.File
	emu        *vt.SafeEmulator
	cols, rows int
	workspace  string
	exited     chan struct{}
}

// launch builds a temp workspace, starts the ycc binary under a PTY pointed at
// the stub, and begins streaming its output into the emulator. It registers all
// teardown with t.Cleanup.
func launch(t *testing.T, script []scriptedTurn) *harness {
	t.Helper()
	if testing.Short() {
		t.Skip("e2e TUI harness skipped under -short")
	}
	if yccBin == "" {
		t.Skip("ycc binary not built (TestMain skipped)")
	}

	stub := newLLMStub(script)
	t.Cleanup(stub.Close)

	ws := t.TempDir()
	home := t.TempDir()
	setupWorkspace(t, ws, stub.URL)

	const cols, rows = 120, 40
	cmd := exec.Command(yccBin)
	cmd.Dir = ws
	cmd.Env = harnessEnv(home)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		// A sandbox without PTY support cannot run this harness.
		t.Skipf("cannot allocate PTY (%v); e2e TUI harness requires one", err)
	}

	h := &harness{
		t: t, stub: stub, cmd: cmd, ptmx: ptmx,
		emu:  vt.NewSafeEmulator(cols, rows),
		cols: cols, rows: rows, workspace: ws,
		exited: make(chan struct{}),
	}

	// Stream PTY output into the emulator until the process closes the pty.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				_, _ = h.emu.Write(buf[:n])
			}
			if rerr != nil {
				return
			}
		}
	}()

	// Feed the emulator's replies (answers to terminal queries the TUI emits —
	// cursor position, device attributes, kitty-keyboard, etc.) back to the PTY.
	// This is essential: the emulator answers such queries by writing to its
	// input pipe, and if nothing drains it that Write blocks while holding the
	// emulator lock, deadlocking every screen read. Piping it back to the child
	// both drains the pipe and gives the real app the query answers it expects.
	go func() {
		buf := make([]byte, 1024)
		for {
			n, rerr := h.emu.Read(buf)
			if n > 0 {
				_, _ = ptmx.Write(buf[:n])
			}
			if rerr != nil {
				return
			}
		}
	}()

	// Reap the process in the background so waitForExit can observe it.
	go func() {
		_ = cmd.Wait()
		close(h.exited)
	}()

	t.Cleanup(h.close)
	return h
}

// setupWorkspace prepares a temp workspace: a git repo, a spec.md, a backlog
// directory, a seeded file for the scripted Read tool call, and a ycc.toml that
// points the daemon at the stub. Because the workspace ycc.toml is discovered
// first, the first-run setup wizard never runs.
func setupWorkspace(t *testing.T, ws, stubURL string) {
	t.Helper()
	run := func(name string, args ...string) {
		c := exec.Command(name, args...)
		c.Dir = ws
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}
	run("git", "init")
	run("git", "config", "user.name", "ycc e2e")
	run("git", "config", "user.email", "e2e@example.com")

	writeFile(t, filepath.Join(ws, "spec.md"), "# E2E project\n\nA throwaway workspace for the ycc e2e TUI harness.\n")
	if err := os.MkdirAll(filepath.Join(ws, "backlog"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Seeded file the scripted Read tool call targets.
	writeFile(t, filepath.Join(ws, "hello.txt"), "hello from the e2e workspace\n")

	toml := fmt.Sprintf(`max_tokens = 4096
max_turns = 20

[models.stub]
backend = "openai"
base_url = %q
model = "stub-model"
key_env = "YCC_E2E_KEY"

[roles]
coordinator = "stub"
implementer = "stub"
reviewers = ["stub"]
`, stubURL)
	writeFile(t, filepath.Join(ws, "ycc.toml"), toml)
}

// harnessEnv returns a minimal, isolated environment: HOME/XDG dirs point at a
// throwaway tree (no real user config, secrets, or cache is read or written),
// the stub key is present, and terminal settings are fixed. Notably
// ANTHROPIC_API_KEY is NOT propagated, so nothing can reach a real backend.
func harnessEnv(home string) []string {
	cfg := filepath.Join(home, ".config")
	cache := filepath.Join(home, ".cache")
	_ = os.MkdirAll(cfg, 0o755)
	_ = os.MkdirAll(cache, 0o755)
	env := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + cfg,
		"XDG_CACHE_HOME=" + cache,
		"TERM=xterm-256color",
		"YCC_E2E_KEY=test-key",
		"COLORTERM=truecolor",
	}
	// Preserve PATH so the real git/bash tools remain available to the daemon.
	if p := os.Getenv("PATH"); p != "" {
		env = append(env, "PATH="+p)
	}
	return env
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// screenText renders the emulator's current cell grid to plain text (rows joined
// by newlines, unset/empty cells as spaces). This is the oracle every assertion
// runs against.
func (h *harness) screenText() string {
	var b strings.Builder
	for y := 0; y < h.rows; y++ {
		for x := 0; x < h.cols; x++ {
			c := h.emu.CellAt(x, y)
			if c == nil || c.Content == "" {
				b.WriteByte(' ')
				continue
			}
			b.WriteString(c.Content)
			if c.Width > 1 {
				x += c.Width - 1
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// waitFor blocks until pred(screenText) is true or the deadline elapses. On
// timeout it dumps the current screen (and writes a diagnostic PNG when the
// snapshot dir is set) and fails the test.
func (h *harness) waitFor(desc string, pred func(string) bool) {
	h.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if pred(h.screenText()) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.screenshot("timeout")
	h.t.Fatalf("timed out waiting for %s\n--- screen ---\n%s\n--------------", desc, h.screenText())
}

// waitForText waits until the screen contains substr.
func (h *harness) waitForText(substr string) {
	h.t.Helper()
	h.waitFor(fmt.Sprintf("text %q", substr), func(s string) bool {
		return strings.Contains(s, substr)
	})
}

// waitForRegex waits until the screen matches re (a regexp over the whole grid).
func (h *harness) waitForRegex(re *regexp.Regexp) {
	h.t.Helper()
	h.waitFor(fmt.Sprintf("regex %q", re), func(s string) bool {
		return re.MatchString(s)
	})
}

// send writes raw bytes to the PTY (keystrokes / escape sequences).
func (h *harness) send(s string) {
	h.t.Helper()
	if _, err := h.ptmx.WriteString(s); err != nil {
		h.t.Fatalf("write to pty: %v", err)
	}
}

// resize changes the PTY window size (which delivers SIGWINCH to the child so
// Bubble Tea reflows) and the emulator's grid to match.
func (h *harness) resize(cols, rows int) {
	h.t.Helper()
	if err := pty.Setsize(h.ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}); err != nil {
		h.t.Fatalf("pty resize: %v", err)
	}
	h.cols, h.rows = cols, rows
	h.emu.Resize(cols, rows)
}

// Common key byte sequences for xterm-compatible terminals.
const (
	keyEnter = "\r"
	keyCtrlC = "\x03"
	keyEsc   = "\x1b"
	keyUp    = "\x1b[A"
	keyDown  = "\x1b[B"
	keyLeft  = "\x1b[D"
	keyRight = "\x1b[C"
)

// screenshot rasterizes the current emulator screen to a PNG under
// YCC_TUI_SNAPSHOT_DIR (no-op when unset). Screenshots are artifacts for human /
// agent inspection, never assertion oracles.
func (h *harness) screenshot(name string) {
	dir := os.Getenv("YCC_TUI_SNAPSHOT_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		h.t.Logf("screenshot mkdir %s: %v", dir, err)
		return
	}
	path := filepath.Join(dir, "e2e_"+name+".png")
	if err := snapshot.WriteScreenPNG(path, h.emu, h.cols, h.rows); err != nil {
		h.t.Logf("screenshot %s: %v", path, err)
		return
	}
	h.t.Logf("wrote e2e screenshot %s", path)
}

// waitForExit blocks until the ycc process exits or the timeout elapses.
func (h *harness) waitForExit(d time.Duration) bool {
	select {
	case <-h.exited:
		return true
	case <-time.After(d):
		return false
	}
}

// close tears the harness down: it sends ctrl+c (the quit chord), gives the
// process a moment to exit cleanly, then force-kills and closes the PTY.
func (h *harness) close() {
	if h.ptmx != nil {
		_, _ = h.ptmx.WriteString(keyCtrlC)
	}
	if !h.waitForExit(3 * time.Second) {
		if h.cmd != nil && h.cmd.Process != nil {
			_ = h.cmd.Process.Kill()
		}
		h.waitForExit(2 * time.Second)
	}
	if h.ptmx != nil {
		_ = h.ptmx.Close()
	}
}

// ensure the emulator satisfies the snapshot rasterizer's grid contract at
// compile time (SafeEmulator.CellAt returns *uv.Cell).
var _ snapshot.Grid = (*vt.SafeEmulator)(nil)
