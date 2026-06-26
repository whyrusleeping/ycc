package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestEditReplaceAll(t *testing.T) {
	root := t.TempDir()
	reg := workerReg(root)
	dispatch(t, reg, "Write", `{"file_path":"a.txt","content":"x x x"}`)

	// Without replace_all, a non-unique old_string is an error.
	res := dispatch(t, reg, "Edit", `{"file_path":"a.txt","old_string":"x","new_string":"y"}`)
	if !res.IsError || !strings.Contains(res.Content, "not unique") {
		t.Fatalf("expected non-unique error, got %q (err=%v)", res.Content, res.IsError)
	}
	// With replace_all, every occurrence is replaced.
	res = dispatch(t, reg, "Edit", `{"file_path":"a.txt","old_string":"x","new_string":"y","replace_all":true}`)
	if res.IsError {
		t.Fatalf("replace_all Edit: %s", res.Content)
	}
	got, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(got) != "y y y" {
		t.Fatalf("after replace_all = %q", got)
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
