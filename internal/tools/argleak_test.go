package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
)

func TestRegistryRejectsLeakedArgumentsWithoutExecuting(t *testing.T) {
	calls := 0
	reg := New()
	reg.Add(&gollama.Tool{
		Name: "ask_user",
		Params: Obj(map[string]any{
			"question": StrProp("question"), "options": StrArrProp("options"),
		}, "question"),
		Call: func(context.Context, any) (*gollama.ToolResult, error) {
			calls++
			return &gollama.ToolResult{Content: "ok"}, nil
		},
	})
	call := gollama.ToolCall{Function: gollama.ToolCallFunction{
		Name: "ask_user", Arguments: `{"question":"Pick one?</question>\n<parameter name=\"options\">[\"a\",\"b\"]"}`,
	}}
	original := call.Function.Arguments
	res := reg.Dispatch(context.Background(), call)
	if !res.IsError || !strings.Contains(res.Content, "resend") || calls != 0 {
		t.Fatalf("leaked call executed or lacked correction: calls=%d result=%+v", calls, res)
	}
	if call.Function.Arguments != original {
		t.Fatal("arguments were rewritten")
	}
	call.Function.Arguments = `{"question":"Pick one?","options":["a","b"]}`
	if res := reg.Dispatch(context.Background(), call); res.IsError || calls != 1 {
		t.Fatalf("corrected call failed: calls=%d result=%+v", calls, res)
	}
}

func TestRejectLeakedArgsPreservesLiteralMarkup(t *testing.T) {
	schema := Obj(map[string]any{
		"question": StrProp("question"), "options": StrArrProp("options"), "content": StrProp("content"),
	})
	for _, raw := range []string{
		`{"question":"see <parameter name=\"options\"> in the docs"}`,
		`{"question":"hi</question>\n<parameter name=\"nonesuch\">x"}`,
		`{"question":"hi</question>\n<parameter name=\"options\">[\"leaked\"]","options":["real"]}`,
		`{"content":"Write it as </foo>\n<parameter name=\"bar\">baz"}`,
	} {
		if err := rejectLeakedArgs(raw, schema); err != nil {
			t.Fatalf("literal markup rejected: %s: %v", raw, err)
		}
	}
}
