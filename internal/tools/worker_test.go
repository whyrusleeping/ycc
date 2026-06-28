package tools

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
)

func dispatch(t *testing.T, reg *Registry, name, args string) *gollama.ToolResult {
	t.Helper()
	return reg.Dispatch(context.Background(), gollama.ToolCall{
		ID: "x", Type: "function",
		Function: gollama.ToolCallFunction{Name: name, Arguments: args},
	})
}

func workerReg(root string) *Registry {
	reg := New()
	reg.Add(Worker(&Workspace{Root: root})...)
	return reg
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
	res := dispatch(t, reg, "Read", `{"file_path":"../../etc/passwd"}`)
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

func TestUnknownTool(t *testing.T) {
	reg := workerReg(t.TempDir())
	res := dispatch(t, reg, "nope", `{}`)
	if !res.IsError {
		t.Fatal("expected error for unknown tool")
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
