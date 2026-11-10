package e2e

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/daemon"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

// yccBin is the path to the ycc binary built once by TestMain.
var yccBin string

// TestMain builds the ycc binary a single time for the whole package, so every
// scenario drives the identical real artifact. The suite is skipped under
// `go test -short` (it spawns subprocesses and is slower than a unit test).
func TestMain(m *testing.M) {
	// testing.Short() reads a flag, so flags must be parsed before we consult it.
	flag.Parse()
	if testing.Short() {
		// newEnv() also skips each test under -short; returning early here avoids
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

// env is one isolated scenario: a scripted model stub, a git workspace whose
// ycc.toml points at the stub, and a throwaway HOME/XDG tree so no real user
// config, secrets, token file, or state is read or written.
type env struct {
	t         *testing.T
	stub      *llmStub
	workspace string
	home      string
	addr      string // persistent daemon URL once startDaemon ran
	token     string
}

// newEnv prepares the workspace and stub for one scenario.
func newEnv(t *testing.T, script []scriptedTurn) *env {
	t.Helper()
	if testing.Short() {
		t.Skip("e2e harness skipped under -short")
	}
	if yccBin == "" {
		t.Skip("ycc binary not built (TestMain skipped)")
	}
	stub := newLLMStub(script)
	t.Cleanup(stub.Close)
	e := &env{t: t, stub: stub, workspace: t.TempDir(), home: t.TempDir(), token: "e2e-token"}
	setupWorkspace(t, e.workspace, stub.URL)
	return e
}

// setupWorkspace prepares a temp workspace: a git repo, a spec.md, a backlog
// directory, a seeded file for the scripted Read tool call, and a ycc.toml that
// points the daemon at the stub.
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

	writeFile(t, filepath.Join(ws, "spec.md"), "# E2E project\n\nA throwaway workspace for the ycc end-to-end harness.\n")
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

// environ returns a minimal, isolated environment: HOME/XDG dirs point at the
// scenario's throwaway tree and the stub key is present. ANTHROPIC_API_KEY and
// YCC_TOKEN are NOT propagated, so nothing can reach a real backend or daemon.
func (e *env) environ(extra ...string) []string {
	out := []string{
		"HOME=" + e.home,
		"XDG_CONFIG_HOME=" + filepath.Join(e.home, ".config"),
		"XDG_CACHE_HOME=" + filepath.Join(e.home, ".cache"),
		"XDG_STATE_HOME=" + filepath.Join(e.home, ".state"),
		"YCC_E2E_KEY=test-key",
	}
	// Preserve PATH so the real git/bash tools remain available to the daemon.
	if p := os.Getenv("PATH"); p != "" {
		out = append(out, "PATH="+p)
	}
	return append(out, extra...)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// freePort reserves an ephemeral loopback port for the daemon to bind.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// startDaemon runs `ycc daemon` (the persistent foreground service) on a free
// loopback port with an explicit token and waits until it answers RPCs.
func (e *env) startDaemon() {
	e.t.Helper()
	hostport := freePort(e.t)
	p := e.spawn(e.environ("YCC_TOKEN="+e.token), "daemon", "--addr", hostport, "--workspace", e.workspace)
	e.addr = "http://" + hostport
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if daemon.Reachable(e.addr, e.token) {
			return
		}
		select {
		case <-p.exited:
			e.t.Fatalf("daemon exited before becoming ready\n%s", p.output())
		case <-time.After(100 * time.Millisecond):
		}
	}
	e.t.Fatalf("daemon not reachable at %s\n%s", e.addr, p.output())
}

// client dials the persistent daemon the way remote clients do.
func (e *env) client() yccv1connect.SessionServiceClient {
	return daemon.DialClient(e.addr, e.token)
}

// cli runs a short ycc CLI command against the persistent daemon and returns
// its combined output, failing the test on a non-zero exit.
func (e *env) cli(args ...string) string {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, yccBin, append([]string{"--addr", e.addr}, args...)...)
	cmd.Dir = e.workspace
	cmd.Env = e.environ("YCC_TOKEN=" + e.token)
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.t.Fatalf("ycc %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// proc is a long-running ycc process (a daemon or a streaming `ycc start`)
// whose combined stdout/stderr is captured line by line.
type proc struct {
	t      *testing.T
	cmd    *exec.Cmd
	mu     sync.Mutex
	buf    strings.Builder
	exited chan struct{}
}

// spawn starts ycc with args in the workspace (stdin is /dev/null). The
// process is stopped with SIGTERM, then killed, at test cleanup.
func (e *env) spawn(environ []string, args ...string) *proc {
	e.t.Helper()
	cmd := exec.Command(yccBin, args...)
	cmd.Dir = e.workspace
	cmd.Env = environ
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		e.t.Fatalf("start ycc %v: %v", args, err)
	}
	p := &proc{t: e.t, cmd: cmd, exited: make(chan struct{})}
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			p.mu.Lock()
			p.buf.WriteString(sc.Text())
			p.buf.WriteByte('\n')
			p.mu.Unlock()
		}
	}()
	go func() {
		_ = cmd.Wait()
		_ = pw.Close()
		close(p.exited)
	}()
	e.t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-p.exited:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-p.exited
		}
	})
	return p
}

func (p *proc) output() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buf.String()
}

// waitFor blocks until the captured output contains substr, failing the test
// (with the output so far) after 20s or if the process exits first.
func (p *proc) waitFor(substr string) {
	p.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(p.output(), substr) {
			return
		}
		select {
		case <-p.exited:
			if strings.Contains(p.output(), substr) {
				return
			}
			p.t.Fatalf("ycc exited before printing %q\n--- output ---\n%s", substr, p.output())
		case <-time.After(50 * time.Millisecond):
		}
	}
	p.t.Fatalf("timed out waiting for %q\n--- output ---\n%s", substr, p.output())
}

// sessionID returns the id `ycc start` prints on its first line.
func (p *proc) sessionID() string {
	p.t.Helper()
	p.waitFor("session s_")
	for _, line := range strings.Split(p.output(), "\n") {
		if id, ok := strings.CutPrefix(line, "session "); ok {
			return strings.TrimSpace(id)
		}
	}
	p.t.Fatalf("no session id in output\n%s", p.output())
	return ""
}

// answer replies to the session's pending question by option index over the
// daemon API, as the web and iOS clients do.
func (e *env) answer(sessionID string, option int32) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := e.client().AnswerQuestion(ctx, connect.NewRequest(&v1.AnswerQuestionRequest{SessionId: sessionID, OptionIndex: option})); err != nil {
		e.t.Fatalf("AnswerQuestion: %v", err)
	}
}
