package tui

import (
	"strings"
	"testing"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// A tool_result carrying a structured view renders as a connector tree (summary
// headline + nested nodes) instead of the raw text, inside the expanded card.
func TestToolViewTreeRendering(t *testing.T) {
	m := &model{w: 90, expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1}
	view := `{"summary":"1/2 reviewers accept","status":"warn","nodes":[` +
		`{"label":"claude","detail":"accept","kind":"ok"},` +
		`{"label":"gpt","detail":"reject","kind":"error","children":[{"label":"off-by-one","detail":"[blocker]","kind":"error"}]}]}`
	m.evs = []*v1.Event{
		{Seq: 1, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"c1","name":"re_review","args":"{\"task_id\":\"0042\"}"}`},
		{Seq: 2, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"c1","result":"RAWTEXT","view":` + view + `}`},
	}
	m.expanded[1] = true
	out := m.renderBlock(0, m.evs[0])
	for _, want := range []string{"1/2 reviewers accept", "claude", "├─", "└─", "off-by-one", "[blocker]"} {
		if !strings.Contains(out, want) {
			t.Fatalf("view tree missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "RAWTEXT") {
		t.Fatalf("view present but raw result text still rendered:\n%s", out)
	}
	// No view => raw text path still works.
	if toolViewOf(&v1.Event{DataJson: `{"result":"x"}`}) != nil {
		t.Fatal("toolViewOf should be nil without a view field")
	}
}

// argField/callFor correlate a tool_result with its originating tool_call so the
// renderer can infer language from the call's args.
func TestCallForAndArgField(t *testing.T) {
	call := &v1.Event{Seq: 1, Type: "tool_call", DataJson: `{"name":"Read","args":"{\"file_path\":\"x.go\"}","id":"c1"}`}
	res := &v1.Event{Seq: 2, Type: "tool_result", DataJson: `{"name":"Read","result":"...","id":"c1"}`}
	m := &model{evs: []*v1.Event{call, res}}
	if got := m.callFor(res); got != call {
		t.Fatalf("callFor did not match by id")
	}
	if got := argField(call, "file_path"); got != "x.go" {
		t.Fatalf("argField(file_path) = %q, want x.go", got)
	}
	// Fallback to nearest preceding tool_call when id is absent.
	res2 := &v1.Event{Seq: 2, Type: "tool_result", DataJson: `{"name":"Read","result":"..."}`}
	m2 := &model{evs: []*v1.Event{call, res2}}
	if got := m2.callFor(res2); got != call {
		t.Fatalf("callFor fallback to preceding tool_call failed")
	}
}

func TestPrettyArgs(t *testing.T) {
	out := prettyArgs(`{"file_path":"a.go","content":"x"}`)
	if !strings.Contains(out, "\n") || !strings.Contains(out, "file_path") {
		t.Fatalf("prettyArgs should indent JSON:\n%s", out)
	}
	if prettyArgs("not json") != "not json" {
		t.Fatal("prettyArgs should pass through non-JSON")
	}
}

func TestDetailLineToolCall(t *testing.T) {
	ev := &v1.Event{Type: "tool_call", DataJson: `{"name":"Read","args":"{\"file_path\":\"x\"}"}`}
	if d := detailLine(ev); !strings.HasPrefix(d, "Read(") {
		t.Fatalf("detailLine = %q", d)
	}
}

// durationMSField extracts duration_ms from an event's data JSON, tolerating
// missing fields and malformed JSON.
func TestDurationMSField(t *testing.T) {
	if got := durationMSField(&v1.Event{DataJson: `{"duration_ms":340}`}); got != 340 {
		t.Errorf("duration_ms present = %d, want 340", got)
	}
	if got := durationMSField(&v1.Event{DataJson: `{"text":"hi"}`}); got != 0 {
		t.Errorf("duration_ms absent = %d, want 0", got)
	}
	if got := durationMSField(&v1.Event{DataJson: ``}); got != 0 {
		t.Errorf("empty data = %d, want 0", got)
	}
	if got := durationMSField(&v1.Event{DataJson: `not json`}); got != 0 {
		t.Errorf("bad json = %d, want 0", got)
	}
}

func TestEditCardParamsDiff(t *testing.T) {
	m := &model{w: 100}
	args := `{"file_path":"x.go","old_string":"foo\nbar\nbaz","new_string":"foo\nqux\nbaz"}`
	call := &v1.Event{Seq: 1, Type: "tool_call", Actor: "coordinator",
		DataJson: `{"id":"c1","name":"Edit","args":` + mustJSONString(args) + `}`}
	out := stripANSI(m.cardParams(call))
	if !strings.Contains(out, "-bar") || !strings.Contains(out, "+qux") {
		t.Errorf("expected diff lines, got:\n%s", out)
	}
	if !strings.Contains(out, "@@") {
		t.Errorf("expected hunk header, got:\n%s", out)
	}
	if strings.Contains(out, "old_string:") || strings.Contains(out, "new_string:") {
		t.Errorf("expected raw key labels to be suppressed, got:\n%s", out)
	}
	if !strings.Contains(out, "x.go") {
		t.Errorf("expected file_path shown, got:\n%s", out)
	}
}
