package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/workspacelease"
)

func TestGetIntExported(t *testing.T) {
	params := map[string]any{"json": float64(7), "native": 9, "bad": "10"}
	if got := GetInt(params, "json", 1); got != 7 {
		t.Fatalf("GetInt float64 = %d, want 7", got)
	}
	if got := GetInt(params, "native", 1); got != 9 {
		t.Fatalf("GetInt int = %d, want 9", got)
	}
	if got := GetInt(params, "bad", 3); got != 3 {
		t.Fatalf("GetInt invalid = %d, want default 3", got)
	}
}

func dispatch(t *testing.T, reg *Registry, name, args string) *gollama.ToolResult {
	t.Helper()
	return reg.Dispatch(context.Background(), gollama.ToolCall{
		ID: "x", Type: "function",
		Function: gollama.ToolCallFunction{Name: name, Arguments: args},
	})
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func workerReg(root string) *Registry {
	reg := New()
	reg.Add(Worker(&Workspace{Root: root})...)
	return reg
}

func TestFileToolsHonorDelegatedMutationScope(t *testing.T) {
	root := t.TempDir()
	ownership := workspacelease.NewService()
	workerToken := ownership.NewToken("session one implementer")
	lifetime, err := ownership.Acquire(root, workerToken)
	if err != nil {
		t.Fatal(err)
	}
	worker := New()
	worker.Add(Worker(&Workspace{Root: root, Ownership: ownership, MutationToken: workerToken})...)
	coordinator := New()
	coordinator.Add(Worker(&Workspace{Root: root, Ownership: ownership, MutationToken: ownership.NewToken("session two coordinator")})...)

	if got := dispatch(t, worker, "Write", `{"file_path":"owned","content":"worker"}`); got.IsError {
		t.Fatalf("worker could not reenter its own lease: %s", got.Content)
	}
	if got := dispatch(t, coordinator, "Edit", `{"file_path":"owned","old_string":"worker","new_string":"other"}`); !got.IsError || !strings.Contains(got.Content, "session one implementer") {
		t.Fatalf("file tool did not identify delegated owner: %+v", got)
	}
	lifetime.Release()
	if got := dispatch(t, coordinator, "Edit", `{"file_path":"owned","old_string":"worker","new_string":"other"}`); got.IsError {
		t.Fatalf("file tool remained blocked after worker termination: %s", got.Content)
	}
}

func TestFileToolsLeaseDestinationInExtraWriteRoot(t *testing.T) {
	primary := t.TempDir()
	extra := t.TempDir()
	if out, err := exec.Command("git", "-C", extra, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	ownership := workspacelease.NewService()
	owner := ownership.NewToken("session two coordinator")
	lifetime, err := ownership.Acquire(extra, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer lifetime.Release()

	alias := filepath.Join(primary, "external")
	if err := os.Symlink(extra, alias); err != nil {
		t.Fatal(err)
	}
	first := New()
	first.Add(Worker(&Workspace{
		Root: primary, WriteRoots: []string{extra}, Ownership: ownership,
		MutationToken: ownership.NewToken("session one coordinator"),
	})...)
	destination := filepath.Join(alias, "new", "file.txt")
	got := dispatch(t, first, "Write", fmt.Sprintf(`{"file_path":%q,"content":"wrong"}`, destination))
	if !got.IsError || !strings.Contains(got.Content, owner.Owner()) {
		t.Fatalf("extra-root Write did not honor destination owner: %+v", got)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("refused destination was written: %v", err)
	}
}

func TestWriteReadEdit(t *testing.T) {
	root := t.TempDir()
	reg := workerReg(root)

	if res := dispatch(t, reg, "Write", `{"file_path":"sub/a.txt","content":"hello world"}`); res.IsError {
		t.Fatalf("Write: %s", res.Content)
	}
	if got, err := os.ReadFile(filepath.Join(root, "sub/a.txt")); err != nil || string(got) != "hello world" {
		t.Fatalf("file = %q err=%v", got, err)
	}

	// Read returns cat -n format: a line number, a tab, then the content.
	res := dispatch(t, reg, "Read", `{"file_path":"sub/a.txt"}`)
	if res.IsError || !strings.Contains(res.Content, "\thello world") || !strings.Contains(res.Content, "     1\t") {
		t.Fatalf("Read = %q (err=%v)", res.Content, res.IsError)
	}

	// Edit accepts an absolute file_path within the workspace.
	abs := filepath.Join(root, "sub/a.txt")
	if res := dispatch(t, reg, "Edit", `{"file_path":"`+abs+`","old_string":"world","new_string":"there"}`); res.IsError {
		t.Fatalf("Edit: %s", res.Content)
	}
	got, _ := os.ReadFile(abs)
	if string(got) != "hello there" {
		t.Fatalf("after Edit = %q", got)
	}
}

func TestReadOffsetLimit(t *testing.T) {
	root := t.TempDir()
	reg := workerReg(root)
	dispatch(t, reg, "Write", `{"file_path":"n.txt","content":"l1\nl2\nl3\nl4\nl5"}`)
	res := dispatch(t, reg, "Read", `{"file_path":"n.txt","offset":2,"limit":2}`)
	if res.IsError || !strings.Contains(res.Content, "     2\tl2") || !strings.Contains(res.Content, "     3\tl3") {
		t.Fatalf("offset/limit Read = %q", res.Content)
	}
	if strings.Contains(res.Content, "l1") || strings.Contains(res.Content, "l4") {
		t.Fatalf("offset/limit returned out-of-window lines: %q", res.Content)
	}
}

func TestReadUTF8CRLFWindow(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "utf8.txt"), []byte("alpha\r\nβeta\r\n世界\r\nomega\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := dispatch(t, workerReg(root), "Read", `{"file_path":"utf8.txt","offset":2,"limit":2}`)
	if res.IsError {
		t.Fatalf("Read UTF-8/CRLF: %s", res.Content)
	}
	want := "     2\tβeta\r\n     3\t世界\r\n"
	if res.Content != want {
		t.Fatalf("Read UTF-8/CRLF = %q, want %q", res.Content, want)
	}
}

func TestReadUnterminatedLineAtExactBufferBoundary(t *testing.T) {
	root := t.TempDir()
	content := strings.Repeat("x", binarySampleBytes)
	if err := os.WriteFile(filepath.Join(root, "exact.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := workerReg(root)
	res := dispatch(t, reg, "Read", `{"file_path":"exact.txt","limit":1}`)
	if res.IsError || !strings.HasPrefix(res.Content, "     1\t") || !strings.Contains(res.Content, "[line truncated]") {
		t.Fatalf("Read exact-buffer final line = %q (err=%v)", res.Content, res.IsError)
	}
	res = dispatch(t, reg, "Read", `{"file_path":"exact.txt","offset":2,"limit":1}`)
	if res.IsError || res.Content != "(offset 2 is past end of file; 1 lines total)" {
		t.Fatalf("Read past exact-buffer final line = %q (err=%v)", res.Content, res.IsError)
	}
}

func TestReadHugeFileOnlyReadsWindow(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "huge.txt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "first\n" + strings.Repeat("x", binarySampleBytes)
	if _, err := f.WriteString(prefix); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Truncate(512 * 1024 * 1024); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	res := dispatch(t, workerReg(root), "Read", `{"file_path":"huge.txt","limit":1}`)
	if res.IsError || res.Content != "     1\tfirst\n" {
		t.Fatalf("Read huge window = %q (err=%v)", res.Content, res.IsError)
	}
}

func TestReadLongLineAndSourceLimit(t *testing.T) {
	root := t.TempDir()
	long := strings.Repeat("界", maxReadLineBytes) + "\nnext\n"
	if err := os.WriteFile(filepath.Join(root, "long.txt"), []byte(long), 0o644); err != nil {
		t.Fatal(err)
	}
	res := dispatch(t, workerReg(root), "Read", `{"file_path":"long.txt","limit":2}`)
	if res.IsError || !strings.Contains(res.Content, "[line truncated]") || !strings.Contains(res.Content, "     2\tnext") {
		t.Fatalf("Read long line = %q (err=%v)", res.Content, res.IsError)
	}
	if !utf8.ValidString(res.Content) {
		t.Fatalf("Read split UTF-8 in long line: %q", res.Content)
	}

	path := filepath.Join(root, "scan.txt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	chunk := strings.Repeat("a", 64*1024)
	for written := 0; written <= maxReadSourceBytes; written += len(chunk) {
		if _, err := f.WriteString(chunk); err != nil {
			f.Close()
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	res = dispatch(t, workerReg(root), "Read", `{"file_path":"scan.txt","offset":2,"limit":1}`)
	if res.IsError || !strings.Contains(res.Content, "source scan stopped at 8388608 bytes") {
		t.Fatalf("Read source limit = %q (err=%v)", res.Content, res.IsError)
	}
}

func TestReadBinaryReturnsMetadata(t *testing.T) {
	root := t.TempDir()
	data := []byte{0x7f, 'E', 'L', 'F', 0, 1, 2, 0xff}
	if err := os.WriteFile(filepath.Join(root, "program.bin"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	res := dispatch(t, workerReg(root), "Read", `{"file_path":"program.bin"}`)
	if res.IsError || !strings.Contains(res.Content, "contains binary data") ||
		!strings.Contains(res.Content, "size 8 bytes") || !strings.Contains(res.Content, "detected type") ||
		!strings.Contains(res.Content, "xxd -l 256") {
		t.Fatalf("Read binary = %q (err=%v)", res.Content, res.IsError)
	}
	if strings.Contains(res.Content, string(data)) {
		t.Fatalf("Read returned binary payload: %q", res.Content)
	}
}

func TestReadRejectsSpecialFilesPromptly(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("FIFO not supported: %v", err)
	}
	reg := workerReg(root)
	done := make(chan *gollama.ToolResult, 1)
	go func() {
		done <- reg.Dispatch(context.Background(), gollama.ToolCall{
			ID: "fifo", Type: "function",
			Function: gollama.ToolCallFunction{Name: "Read", Arguments: `{"file_path":"pipe"}`},
		})
	}()
	select {
	case res := <-done:
		if !res.IsError || !strings.Contains(res.Content, "not a regular file") || !strings.Contains(res.Content, "Use Bash") {
			t.Fatalf("Read FIFO = %q (err=%v)", res.Content, res.IsError)
		}
	case <-time.After(time.Second):
		t.Fatal("Read blocked opening a FIFO")
	}

	res := dispatch(t, reg, "Read", `{"file_path":"`+os.DevNull+`"}`)
	if !res.IsError || !strings.Contains(res.Content, "not a regular file") {
		t.Fatalf("Read device = %q (err=%v)", res.Content, res.IsError)
	}
}

func TestReadHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := workerReg(root).Dispatch(ctx, gollama.ToolCall{
		ID: "cancel", Type: "function",
		Function: gollama.ToolCallFunction{Name: "Read", Arguments: `{"file_path":"file.txt"}`},
	})
	if !res.IsError || !strings.Contains(res.Content, context.Canceled.Error()) {
		t.Fatalf("canceled Read = %q (err=%v)", res.Content, res.IsError)
	}
}

func TestReadCancellationDuringRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	source := readerFunc(func(p []byte) (int, error) {
		calls++
		n := copy(p, "hello\n")
		cancel()
		return n, nil
	})
	res := readTextWindow(ctx, source, int64(len("hello\n")), "cancel.txt", 1, 1)
	if !res.IsError || !strings.Contains(res.Content, context.Canceled.Error()) {
		t.Fatalf("Read canceled during source read = %q (err=%v)", res.Content, res.IsError)
	}
	if calls != 1 {
		t.Fatalf("source Read calls = %d, want 1", calls)
	}
}

func TestEditUniqueMatch(t *testing.T) {
	root := t.TempDir()
	reg := workerReg(root)
	dispatch(t, reg, "Write", `{"file_path":"a.txt","content":"x x x"}`)

	// A non-unique old_string is an error and must not modify the file.
	res := dispatch(t, reg, "Edit", `{"file_path":"a.txt","old_string":"x","new_string":"y"}`)
	if !res.IsError || !strings.Contains(res.Content, "not unique") || !strings.Contains(res.Content, "found 3 matches") {
		t.Fatalf("expected non-unique error, got %q (err=%v)", res.Content, res.IsError)
	}
	if !strings.Contains(res.Content, "context") {
		t.Fatalf("multi-match error should guide to add context, got %q", res.Content)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(got) != "x x x" {
		t.Fatalf("file should be unchanged after multi-match error, got %q", got)
	}

	// A zero-match old_string returns a clear not-found error.
	res = dispatch(t, reg, "Edit", `{"file_path":"a.txt","old_string":"zzz","new_string":"y"}`)
	if !res.IsError || !strings.Contains(res.Content, "not found") {
		t.Fatalf("expected not-found error, got %q (err=%v)", res.Content, res.IsError)
	}

	// A unique old_string succeeds and applies the replacement.
	dispatch(t, reg, "Write", `{"file_path":"b.txt","content":"foo bar baz"}`)
	res = dispatch(t, reg, "Edit", `{"file_path":"b.txt","old_string":"bar","new_string":"qux"}`)
	if res.IsError {
		t.Fatalf("unique Edit: %s", res.Content)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "b.txt")); string(got) != "foo qux baz" {
		t.Fatalf("after unique Edit = %q", got)
	}
}

func TestPathConfinement(t *testing.T) {
	root := t.TempDir()
	reg := workerReg(root)
	// Reads are unrestricted, but writes stay confined to the workspace.
	res := dispatch(t, reg, "Write", `{"file_path":"../../etc/ycc-test-escape","content":"x"}`)
	if !res.IsError || !strings.Contains(res.Content, "outside the workspace") {
		t.Fatalf("expected confinement rejection, got %q (err=%v)", res.Content, res.IsError)
	}
}

func TestBash(t *testing.T) {
	root := t.TempDir()
	reg := workerReg(root)
	res := dispatch(t, reg, "Bash", `{"command":"echo hi > out.txt && cat out.txt"}`)
	if res.IsError || !strings.Contains(res.Content, "hi") {
		t.Fatalf("bash = %q (err=%v)", res.Content, res.IsError)
	}
	if _, err := os.Stat(filepath.Join(root, "out.txt")); err != nil {
		t.Fatalf("bash ran outside workspace root: %v", err)
	}
}

func TestBashWorkspaceEnv(t *testing.T) {
	reg := New()
	reg.Add(Worker(&Workspace{Root: t.TempDir(), Env: []string{"YCC_WORKTREE_ENV=visible"}})...)
	res := dispatch(t, reg, "Bash", `{"command":"printf '%s' \"$YCC_WORKTREE_ENV\""}`)
	if res.IsError || res.Content != "visible" {
		t.Fatalf("bash workspace env = %q (err=%v)", res.Content, res.IsError)
	}
}

func TestBashCustomForegroundTimeout(t *testing.T) {
	reg := workerReg(t.TempDir())
	start := time.Now()
	res := dispatch(t, reg, "Bash", `{"command":"sleep 2","timeout_s":1}`)
	if res.IsError || !strings.Contains(res.Content, "command timed out after 1s") {
		t.Fatalf("bash timeout = %q (err=%v)", res.Content, res.IsError)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("custom timeout took too long: %s", elapsed)
	}
}

func TestBashTimeoutSchemaAndValidation(t *testing.T) {
	reg := workerReg(t.TempDir())
	var bashDef *gollama.Tool
	for _, td := range reg.tools {
		if td.Name == "Bash" {
			bashDef = td
			break
		}
	}
	if bashDef == nil {
		t.Fatal("no Bash tool")
	}
	params, ok := bashDef.Params.(gollama.ToolFunctionParams)
	if !ok || params.Properties["timeout_s"] == nil {
		t.Fatalf("Bash does not advertise timeout_s: %#v", bashDef.Params)
	}
	for _, args := range []string{
		`{"command":"echo hi","timeout_s":0}`,
		`{"command":"echo hi","timeout_s":3601}`,
	} {
		res := dispatch(t, reg, "Bash", args)
		if !res.IsError || !strings.Contains(res.Content, "between 1 and 3600") {
			t.Fatalf("invalid timeout result = %q (err=%v)", res.Content, res.IsError)
		}
	}
}

// TestBashSurvivesEscapedGrandchild guards the hang where a command's grandchild
// escapes the process group via setsid and inherits the tool's stdout pipe, so
// CombinedOutput's read never reaches EOF and blocks long past the shell's exit
// (golang/go#23019). The shell returns immediately; the backgrounded setsid sleep
// keeps the pipe's write end open. With WaitDelay the dispatch must return
// promptly anyway rather than waiting out the grandchild.
func TestBashSurvivesEscapedGrandchild(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the bash tool's WaitDelay")
	}
	reg := workerReg(t.TempDir())
	done := make(chan *gollama.ToolResult, 1)
	go func() {
		// `setsid sleep 30 &` runs sleep in a new session (so the timeout's
		// process-group kill can't reach it) while inheriting the tool's stdout
		// pipe; the shell exits right after `echo`.
		done <- dispatch(t, reg, "Bash", `{"command":"setsid sleep 30 & echo started"}`)
	}()
	select {
	case res := <-done:
		if res.IsError || !strings.Contains(res.Content, "started") {
			t.Fatalf("bash = %q (err=%v)", res.Content, res.IsError)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("Bash dispatch hung on a grandchild holding the output pipe open")
	}
}

func TestFinishIsControl(t *testing.T) {
	root := t.TempDir()
	reg := workerReg(root)
	res := dispatch(t, reg, "finish", `{"report":"done"}`)
	ctrl := ControlOf(res)
	if ctrl == nil || !ctrl.Stop || ctrl.Report != "done" {
		t.Fatalf("finish control = %+v", ctrl)
	}
}

func TestRequestIntegrationIsControl(t *testing.T) {
	reg := New()
	reg.Add(RequestIntegration())
	res := dispatch(t, reg, "request_integration", `{"report":"rebased and green"}`)
	ctrl := ControlOf(res)
	if ctrl == nil || !ctrl.Stop || ctrl.Blocked || ctrl.Report != "rebased and green" {
		t.Fatalf("request_integration control = %+v", ctrl)
	}
}

func TestUnknownTool(t *testing.T) {
	reg := workerReg(t.TempDir())
	res := dispatch(t, reg, "nope", `{}`)
	if !res.IsError {
		t.Fatal("expected error for unknown tool")
	}
}

func TestReportBlockedIsControl(t *testing.T) {
	root := t.TempDir()
	reg := workerReg(root)

	// report_blocked with a reason stops the loop and marks the run blocked.
	res := dispatch(t, reg, "report_blocked", `{"reason":"which auth scheme?"}`)
	ctrl := ControlOf(res)
	if ctrl == nil || !ctrl.Stop || !ctrl.Blocked || ctrl.Report != "which auth scheme?" {
		t.Fatalf("report_blocked control = %+v", ctrl)
	}

	// Missing/empty reason is an error result, not a control stop.
	res = dispatch(t, reg, "report_blocked", `{}`)
	if !res.IsError || ControlOf(res) != nil {
		t.Fatalf("report_blocked without reason = %q (err=%v, ctrl=%+v)", res.Content, res.IsError, ControlOf(res))
	}
}

// 1x1 transparent PNG.
var tinyPNG = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89, 0x00, 0x00, 0x00,
	0x0A, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00, 0x00, 0x00, 0x00, 0x49,
	0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
}

func TestReadImageReturnsContentBlock(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "pic.png"), tinyPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	reg := workerReg(root)
	res := dispatch(t, reg, "Read", `{"file_path":"pic.png"}`)
	if res.IsError {
		t.Fatalf("Read image: %s", res.Content)
	}
	if len(res.Images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(res.Images))
	}
	if got, err := base64.StdEncoding.DecodeString(res.Images[0]); err != nil || len(got) != len(tinyPNG) {
		t.Fatalf("image payload roundtrip failed: err=%v len=%d", err, len(got))
	}
	if !strings.Contains(res.Content, "image/png") {
		t.Fatalf("expected media-type note in content, got %q", res.Content)
	}
}

func TestReadPDFReturnsDocument(t *testing.T) {
	root := t.TempDir()
	// Minimal PDF header is enough; the tool only base64-encodes the bytes.
	if err := os.WriteFile(filepath.Join(root, "doc.pdf"), []byte("%PDF-1.4\n%%EOF\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := workerReg(root)
	res := dispatch(t, reg, "Read", `{"file_path":"doc.pdf"}`)
	if res.IsError {
		t.Fatalf("Read pdf: %s", res.Content)
	}
	if len(res.Documents) != 1 || res.Documents[0].MediaType != "application/pdf" {
		t.Fatalf("expected 1 pdf document, got %+v", res.Documents)
	}
	if res.Documents[0].Base64 == "" {
		t.Fatal("expected base64 document payload")
	}
}

func TestReadOversizeMediaErrors(t *testing.T) {
	root := t.TempDir()
	big := make([]byte, maxMediaBytes+1)
	copy(big, tinyPNG)
	if err := os.WriteFile(filepath.Join(root, "huge.png"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	reg := workerReg(root)
	res := dispatch(t, reg, "Read", `{"file_path":"huge.png"}`)
	if !res.IsError || len(res.Images) != 0 {
		t.Fatalf("expected oversize error, got err=%v images=%d", res.IsError, len(res.Images))
	}
}

// TestReadDirectoryLists confirms Read on a directory returns its immediate
// entries (not an error) and marks subdirectories with a trailing '/'.
func TestReadDirectoryLists(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	reg := workerReg(root)
	res := dispatch(t, reg, "Read", `{"file_path":"."}`)
	if res.IsError {
		t.Fatalf("Read dir: %s", res.Content)
	}
	if !strings.Contains(res.Content, "subdir/") {
		t.Fatalf("expected subdir marked with trailing slash, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "file.txt") || strings.Contains(res.Content, "file.txt/") {
		t.Fatalf("expected plain file entry, got %q", res.Content)
	}
}

// TestReadDirectoryTruncates confirms a directory with more than maxDirEntries
// entries is truncated with a clear indicator.
func TestReadDirectoryTruncates(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "big")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxDirEntries+5; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%05d", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg := workerReg(root)
	res := dispatch(t, reg, "Read", `{"file_path":"big"}`)
	if res.IsError {
		t.Fatalf("Read big dir: %s", res.Content)
	}
	if !strings.Contains(res.Content, "[listing truncated after at most 1000 entries]") {
		t.Fatalf("expected bounded truncation indicator, got %q", res.Content)
	}
}
