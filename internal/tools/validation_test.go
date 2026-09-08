package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/jobs"
)

func TestDispatchRejectsInvalidDestructiveArguments(t *testing.T) {
	root := t.TempDir()
	reg := workerReg(root)
	path := filepath.Join(root, "existing.txt")
	const original = "keep this text"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		tool  string
		args  string
		field string
	}{
		{"Write missing content", "Write", `{"file_path":"existing.txt"}`, "content"},
		{"Write non-string content", "Write", `{"file_path":"existing.txt","content":false}`, "content"},
		{"Edit missing replacement", "Edit", `{"file_path":"existing.txt","old_string":"keep"}`, "new_string"},
		{"Edit non-string replacement", "Edit", `{"file_path":"existing.txt","old_string":"keep","new_string":7}`, "new_string"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			res := dispatch(t, reg, test.tool, test.args)
			if !res.IsError || !strings.Contains(res.Content, test.field) {
				t.Fatalf("result = %q (error=%v), want field-specific validation error", res.Content, res.IsError)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != original {
				t.Fatalf("invalid call changed file: content=%q err=%v", got, err)
			}
		})
	}
}

func TestDispatchAllowsExplicitEmptyDestructiveStrings(t *testing.T) {
	root := t.TempDir()
	reg := workerReg(root)

	res := dispatch(t, reg, "Write", `{"file_path":"empty.txt","content":""}`)
	if res.IsError {
		t.Fatalf("empty Write content rejected: %s", res.Content)
	}
	if data, err := os.ReadFile(filepath.Join(root, "empty.txt")); err != nil || len(data) != 0 {
		t.Fatalf("empty file = %q, err=%v", data, err)
	}

	path := filepath.Join(root, "edit.txt")
	if err := os.WriteFile(path, []byte("remove me"), 0o644); err != nil {
		t.Fatal(err)
	}
	res = dispatch(t, reg, "Edit", `{"file_path":"edit.txt","old_string":"remove me","new_string":""}`)
	if res.IsError {
		t.Fatalf("empty Edit replacement rejected: %s", res.Content)
	}
	if data, err := os.ReadFile(path); err != nil || len(data) != 0 {
		t.Fatalf("edited file = %q, err=%v", data, err)
	}
}

func TestDispatchRejectsNullArgumentRoot(t *testing.T) {
	calls := 0
	reg := New()
	reg.Add(&gollama.Tool{
		Name:   "optional_only",
		Params: Obj(map[string]any{"enabled": BoolProp("optional flag")}),
		Call: func(_ context.Context, _ any) (*gollama.ToolResult, error) {
			calls++
			return OkResult("called"), nil
		},
	})

	res := dispatch(t, reg, "optional_only", `null`)
	if !res.IsError || !strings.Contains(res.Content, "JSON object") {
		t.Fatalf("null result = %q (error=%v), want object validation error", res.Content, res.IsError)
	}
	if calls != 0 {
		t.Fatalf("null arguments reached handler %d time(s)", calls)
	}

	res = dispatch(t, reg, "optional_only", `{}`)
	if res.IsError || calls != 1 {
		t.Fatalf("empty object result = %q (error=%v), calls=%d", res.Content, res.IsError, calls)
	}
}

func TestDispatchValidatesNestedCollectionsAndEnums(t *testing.T) {
	reg := New()
	reg.Add(submitReview())

	for _, test := range []struct {
		name  string
		args  string
		field string
	}{
		{
			"nested item type",
			`{"verdict":"revise","summary":"bad finding","findings":[{"severity":"major","message":9}]}`,
			"findings[0].message",
		},
		{
			"nested required field",
			`{"verdict":"revise","summary":"bad finding","findings":[{"severity":"major"}]}`,
			"findings[0].message",
		},
		{
			"top-level enum",
			`{"verdict":"maybe","summary":"uncertain","findings":[]}`,
			"verdict",
		},
		{
			"nested enum",
			`{"verdict":"revise","summary":"bad finding","findings":[{"severity":"urgent","message":"fix"}]}`,
			"findings[0].severity",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			res := dispatch(t, reg, "submit_review", test.args)
			if !res.IsError || !strings.Contains(res.Content, test.field) {
				t.Fatalf("result = %q (error=%v), want validation error for %s", res.Content, res.IsError, test.field)
			}
			if ControlOf(res) != nil {
				t.Fatal("invalid review reached handler")
			}
		})
	}
}

func TestDispatchValidatesBoundsAndBooleanWithoutExecuting(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "ran")
	reg := New()
	reg.Add(Worker(&Workspace{Root: root, Jobs: jobs.NewRegistry()})...)

	for _, test := range []struct {
		name string
		args string
	}{
		{"below minimum", `{"command":"touch ran","timeout_s":0}`},
		{"above maximum", `{"command":"touch ran","timeout_s":3601}`},
		{"string boolean", `{"command":"touch ran","run_in_background":"true"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			res := dispatch(t, reg, "Bash", test.args)
			if !res.IsError {
				t.Fatalf("invalid call succeeded: %q", res.Content)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("invalid call executed command; stat err=%v", err)
			}
		})
	}
}

func TestDispatchKeepsRepairNoteOnValidationError(t *testing.T) {
	reg := New()
	reg.Add(&gollama.Tool{
		Name: "repaired",
		Params: Obj(map[string]any{
			"message": StrProp("message"),
			"count":   map[string]any{"type": "integer"},
		}, "message", "count"),
		Call: func(_ context.Context, _ any) (*gollama.ToolResult, error) {
			t.Fatal("invalid repaired call reached handler")
			return nil, nil
		},
	})
	res := dispatch(t, reg, "repaired", `{"message":"hello</message><parameter name=\"count\">not-a-number"}`)
	if !res.IsError || !strings.Contains(res.Content, "count") || !strings.Contains(res.Content, "was recovered") {
		t.Fatalf("result = %q (error=%v)", res.Content, res.IsError)
	}
}
