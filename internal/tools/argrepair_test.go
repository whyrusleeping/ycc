package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
)

// The exact payload observed in the wild (Anthropic opus, ask_user): the model
// closed `question` with a tag and wrote `options` as markup inside the same JSON
// string, so the user got a wall of raw XML and no option picker.
const leakedAskUser = `{"question":"Replay test result: the corpus does NOT fit. What next?</question>\n<parameter name=\"options\">[\"Run the fitting subset now\", \"Stop benchmarking\", \"Something else — I'll explain\"]"}`

// A second real payload, different tool (create_task): a scalar leaked value.
const leakedCreateTask = `{"description":"Publish protection claims.</description>\n<parameter name=\"priority\">3","status":"todo","title":"publish protected_by"}`

func TestRepairLeakedArgsRecoversOptions(t *testing.T) {
	fixed, recovered := RepairLeakedArgs(leakedAskUser, map[string]bool{
		"question": true, "options": true, "questions": true,
	})
	if len(recovered) != 1 || recovered[0] != "options" {
		t.Fatalf("recovered = %v, want [options]", recovered)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(fixed), &args); err != nil {
		t.Fatalf("repaired args are not JSON: %v", err)
	}
	q, _ := args["question"].(string)
	if want := "Replay test result: the corpus does NOT fit. What next?"; q != want {
		t.Fatalf("question = %q, want %q", q, want)
	}
	opts, ok := args["options"].([]any)
	if !ok || len(opts) != 3 || opts[0] != "Run the fitting subset now" {
		t.Fatalf("options = %#v", args["options"])
	}
}

func TestRepairLeakedArgsRecoversScalar(t *testing.T) {
	fixed, recovered := RepairLeakedArgs(leakedCreateTask, map[string]bool{
		"title": true, "description": true, "status": true, "priority": true,
	})
	if len(recovered) != 1 || recovered[0] != "priority" {
		t.Fatalf("recovered = %v, want [priority]", recovered)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(fixed), &args); err != nil {
		t.Fatal(err)
	}
	if args["description"] != "Publish protection claims." {
		t.Fatalf("description = %q", args["description"])
	}
	if args["priority"] != float64(3) {
		t.Fatalf("priority = %#v, want 3", args["priority"])
	}
	// Untouched siblings survive the rewrite.
	if args["status"] != "todo" || args["title"] != "publish protected_by" {
		t.Fatalf("siblings drifted: %#v", args)
	}
}

func TestRepairLeakedArgsLeavesHonestCallsAlone(t *testing.T) {
	declared := map[string]bool{"question": true, "options": true, "content": true, "path": true}
	cases := map[string]string{
		"clean":              `{"question":"which one?","options":["a","b"]}`,
		"no closing tag":     `{"question":"see <parameter name=\"options\"> in the docs"}`,
		"undeclared param":   `{"question":"hi</question>\n<parameter name=\"nonesuch\">x"}`,
		"already set":        `{"question":"hi</question>\n<parameter name=\"options\">[\"leaked\"]","options":["real"]}`,
		"prose about markup": `{"path":"docs.md","content":"Write it as </foo>\n<parameter name=\"bar\">baz"}`,
		"not json":           `not json at all`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			fixed, recovered := RepairLeakedArgs(raw, declared)
			if len(recovered) != 0 || fixed != raw {
				t.Fatalf("repaired an honest call: recovered=%v fixed=%s", recovered, fixed)
			}
		})
	}
}

// End-to-end through the registry: a leaked ask_user-shaped call reaches the tool
// with real, separate arguments.
func TestRegistryRepairsLeakedCall(t *testing.T) {
	var gotQuestion string
	var gotOptions []string
	reg := New()
	reg.Add(&gollama.Tool{
		Name: "ask_user",
		Params: Obj(map[string]any{
			"question": StrProp("the question"),
			"options":  StrArrProp("suggested answers"),
		}, "question"),
		Call: func(_ context.Context, params any) (*gollama.ToolResult, error) {
			gotQuestion, _ = GetString(params, "question")
			gotOptions = GetStringSlice(params, "options")
			return &gollama.ToolResult{Content: "ok"}, nil
		},
	})

	res := reg.Dispatch(context.Background(), gollama.ToolCall{
		ID:       "toolu_1",
		Function: gollama.ToolCallFunction{Name: "ask_user", Arguments: leakedAskUser},
	})

	if res.IsError {
		t.Fatalf("dispatch failed: %s", res.Content)
	}
	// The model is told it malformed the call so it stops doing it.
	if !strings.Contains(res.Content, "recovered") {
		t.Fatalf("result carries no correction note: %q", res.Content)
	}
	if want := "Replay test result: the corpus does NOT fit. What next?"; gotQuestion != want {
		t.Fatalf("question = %q, want %q", gotQuestion, want)
	}
	if len(gotOptions) != 3 {
		t.Fatalf("options = %v, want 3 recovered choices", gotOptions)
	}
}

func TestRegistryRepairReportsNames(t *testing.T) {
	reg := New()
	reg.Add(&gollama.Tool{
		Name: "ask_user",
		Params: Obj(map[string]any{
			"question": StrProp("the question"),
			"options":  StrArrProp("suggested answers"),
		}, "question"),
		Call: func(context.Context, any) (*gollama.ToolResult, error) { return &gollama.ToolResult{}, nil },
	})
	call := gollama.ToolCall{Function: gollama.ToolCallFunction{Name: "ask_user", Arguments: leakedAskUser}}
	fixed, recovered := reg.Repair(call)
	if len(recovered) != 1 || recovered[0] != "options" {
		t.Fatalf("recovered = %v", recovered)
	}
	if fixed.Function.Arguments == call.Function.Arguments {
		t.Fatal("arguments were not rewritten")
	}
	// Unknown tools are passed through untouched.
	unknown := gollama.ToolCall{Function: gollama.ToolCallFunction{Name: "nope", Arguments: leakedAskUser}}
	if _, rec := reg.Repair(unknown); len(rec) != 0 {
		t.Fatalf("unknown tool repaired: %v", rec)
	}
}
