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

	if res := dispatch(t, reg, "write_file", `{"path":"sub/a.txt","content":"hello world"}`); res.IsError {
		t.Fatalf("write_file: %s", res.Content)
	}
	if got, err := os.ReadFile(filepath.Join(root, "sub/a.txt")); err != nil || string(got) != "hello world" {
		t.Fatalf("file = %q err=%v", got, err)
	}

	res := dispatch(t, reg, "read_file", `{"path":"sub/a.txt"}`)
	if res.IsError || res.Content != "hello world" {
		t.Fatalf("read_file = %q (err=%v)", res.Content, res.IsError)
	}

	if res := dispatch(t, reg, "edit_file", `{"path":"sub/a.txt","old_string":"world","new_string":"there"}`); res.IsError {
		t.Fatalf("edit_file: %s", res.Content)
	}
	got, _ := os.ReadFile(filepath.Join(root, "sub/a.txt"))
	if string(got) != "hello there" {
		t.Fatalf("after edit = %q", got)
	}
}

func TestEditNonUniqueFails(t *testing.T) {
	root := t.TempDir()
	reg := workerReg(root)
	dispatch(t, reg, "write_file", `{"path":"a.txt","content":"x x"}`)
	res := dispatch(t, reg, "edit_file", `{"path":"a.txt","old_string":"x","new_string":"y"}`)
	if !res.IsError || !strings.Contains(res.Content, "not unique") {
		t.Fatalf("expected non-unique error, got %q (err=%v)", res.Content, res.IsError)
	}
}

func TestPathConfinement(t *testing.T) {
	root := t.TempDir()
	reg := workerReg(root)
	res := dispatch(t, reg, "read_file", `{"path":"../../etc/passwd"}`)
	if !res.IsError || !strings.Contains(res.Content, "escape") {
		t.Fatalf("expected escape rejection, got %q (err=%v)", res.Content, res.IsError)
	}
}

func TestGrepAndGlob(t *testing.T) {
	root := t.TempDir()
	reg := workerReg(root)
	dispatch(t, reg, "write_file", `{"path":"a.go","content":"package main\nfunc Foo() {}\n"}`)
	dispatch(t, reg, "write_file", `{"path":"b.go","content":"package main\nfunc Bar() {}\n"}`)

	res := dispatch(t, reg, "grep", `{"pattern":"func Foo"}`)
	if res.IsError || !strings.Contains(res.Content, "a.go:2:") {
		t.Fatalf("grep = %q", res.Content)
	}

	res = dispatch(t, reg, "glob", `{"pattern":"*.go"}`)
	if res.IsError || !strings.Contains(res.Content, "a.go") || !strings.Contains(res.Content, "b.go") {
		t.Fatalf("glob = %q", res.Content)
	}
}

func TestBash(t *testing.T) {
	root := t.TempDir()
	reg := workerReg(root)
	res := dispatch(t, reg, "bash", `{"command":"echo hi > out.txt && cat out.txt"}`)
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
