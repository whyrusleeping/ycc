package export

import (
	"encoding/json"
	"strings"
	"testing"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// ev builds a v1.Event with a JSON-marshalled data payload.
func ev(seq int64, actor, typ string, data map[string]any) *v1.Event {
	dj := ""
	if data != nil {
		b, _ := json.Marshal(data)
		dj = string(b)
	}
	return &v1.Event{Seq: seq, Actor: actor, Type: typ, DataJson: dj}
}

// argsJSON marshals a tool call's args (a JSON string field).
func argsJSON(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}

func countSub(s, sub string) int { return strings.Count(s, sub) }

func TestCollapsedToolBullet(t *testing.T) {
	evs := []*v1.Event{
		ev(1, "coordinator", "tool_call", map[string]any{
			"id": "t1", "name": "Read", "args": argsJSON(map[string]any{"file_path": "internal/foo.go"}),
		}),
		ev(2, "coordinator", "tool_result", map[string]any{
			"id": "t1", "result": "package foo", "duration_ms": float64(1234),
		}),
	}
	md := Markdown(evs, Options{SessionID: "s1"})
	if !strings.Contains(md, "`Read`") {
		t.Fatalf("expected tool name in output:\n%s", md)
	}
	if !strings.Contains(md, "✓") {
		t.Fatalf("expected ok glyph:\n%s", md)
	}
	if !strings.Contains(md, "internal/foo.go") {
		t.Fatalf("expected arg summary:\n%s", md)
	}
	if !strings.Contains(md, "(1.2s)") {
		t.Fatalf("expected duration suffix:\n%s", md)
	}
	// default mode hides the result payload
	if strings.Contains(md, "package foo") {
		t.Fatalf("default mode should not include result payload:\n%s", md)
	}
	// merged result: the tool call is a single bullet line
	if c := countSub(md, "`Read`"); c != 1 {
		t.Fatalf("expected exactly one tool bullet, got %d:\n%s", c, md)
	}
}

func TestFullModePayloads(t *testing.T) {
	evs := []*v1.Event{
		ev(1, "coordinator", "tool_call", map[string]any{
			"id": "t1", "name": "Read", "args": argsJSON(map[string]any{"file_path": "foo.go"}),
		}),
		ev(2, "coordinator", "tool_result", map[string]any{
			"id": "t1", "result": "package foo",
		}),
	}
	md := Markdown(evs, Options{SessionID: "s1", Full: true})
	if !strings.Contains(md, "package foo") {
		t.Fatalf("full mode should include result payload:\n%s", md)
	}
	if !strings.Contains(md, "```json") {
		t.Fatalf("full mode should include args json fence:\n%s", md)
	}
	if !strings.Contains(md, "foo.go") {
		t.Fatalf("full mode should include args content:\n%s", md)
	}
}

func TestErrorResultGlyph(t *testing.T) {
	evs := []*v1.Event{
		ev(1, "coordinator", "tool_call", map[string]any{
			"id": "t1", "name": "Bash", "args": argsJSON(map[string]any{"command": "false"}),
		}),
		ev(2, "coordinator", "tool_result", map[string]any{
			"id": "t1", "result": "boom", "error": true,
		}),
	}
	md := Markdown(evs, Options{SessionID: "s1"})
	if !strings.Contains(md, "✗") {
		t.Fatalf("expected error glyph:\n%s", md)
	}
}

func TestAskUserSingleBlock(t *testing.T) {
	evs := []*v1.Event{
		ev(1, "coordinator", "tool_call", map[string]any{
			"id": "a1", "name": "ask_user", "args": argsJSON(map[string]any{"question": "Proceed?"}),
		}),
		ev(2, "coordinator", "question_asked", map[string]any{"question": "Proceed?"}),
		ev(3, "coordinator", "question_answered", map[string]any{"answer": "Yes, go ahead"}),
		ev(4, "coordinator", "tool_result", map[string]any{"id": "a1", "result": "Yes, go ahead"}),
	}
	md := Markdown(evs, Options{SessionID: "s1"})
	if c := countSub(md, "Proceed?"); c != 1 {
		t.Fatalf("question should appear exactly once, got %d:\n%s", c, md)
	}
	if c := countSub(md, "Yes, go ahead"); c != 1 {
		t.Fatalf("answer should appear exactly once, got %d:\n%s", c, md)
	}
	if strings.Contains(md, "ask_user") {
		t.Fatalf("ask_user plumbing should be hidden:\n%s", md)
	}
	if !strings.Contains(md, "**Q:**") {
		t.Fatalf("expected Q block:\n%s", md)
	}
}

func TestAskUserMultiQuestion(t *testing.T) {
	evs := []*v1.Event{
		ev(1, "coordinator", "tool_call", map[string]any{"id": "a1", "name": "ask_user", "args": "{}"}),
		ev(2, "coordinator", "question_asked", map[string]any{
			"questions": []any{
				map[string]any{"question": "First?"},
				map[string]any{"question": "Second?"},
			},
		}),
		ev(3, "coordinator", "question_answered", map[string]any{
			"answers": []any{"answer one", "answer two"},
		}),
		ev(4, "coordinator", "tool_result", map[string]any{"id": "a1", "result": "done"}),
	}
	md := Markdown(evs, Options{SessionID: "s1"})
	for _, want := range []string{"First?", "Second?", "answer one", "answer two"} {
		if c := countSub(md, want); c != 1 {
			t.Fatalf("%q should appear once, got %d:\n%s", want, c, md)
		}
	}
	if !strings.Contains(md, "1. First?") || !strings.Contains(md, "2. Second?") {
		t.Fatalf("expected numbered prompts:\n%s", md)
	}
}

func TestAskUserAutoAnswer(t *testing.T) {
	evs := []*v1.Event{
		ev(1, "coordinator", "tool_call", map[string]any{"id": "a1", "name": "ask_user", "args": argsJSON(map[string]any{"question": "OK?"})}),
		ev(2, "coordinator", "question_asked", map[string]any{"question": "OK?"}),
		ev(3, "coordinator", "question_answered", map[string]any{"answer": "no human", "auto": true}),
		ev(4, "coordinator", "tool_result", map[string]any{"id": "a1", "result": "no human"}),
	}
	md := Markdown(evs, Options{SessionID: "s1"})
	if !strings.Contains(md, "auto-answered (autonomous mode)") {
		t.Fatalf("expected auto-answer line:\n%s", md)
	}
}

func TestEmptyModelTurnHidden(t *testing.T) {
	evs := []*v1.Event{
		ev(1, "coordinator", "model_turn", map[string]any{"text": ""}),
		ev(2, "coordinator", "model_turn", map[string]any{"text": "Hello world"}),
	}
	md := Markdown(evs, Options{SessionID: "s1"})
	if !strings.Contains(md, "Hello world") {
		t.Fatalf("expected non-empty turn:\n%s", md)
	}
	if c := countSub(md, "**coordinator:**"); c != 1 {
		t.Fatalf("expected one coordinator prefix, got %d:\n%s", c, md)
	}
}

func TestEchoedIdleAndFinalReport(t *testing.T) {
	report := "All done. Task complete."
	evs := []*v1.Event{
		ev(1, "coordinator", "model_turn", map[string]any{"text": report}),
		ev(2, "coordinator", "session_idle", map[string]any{"report": report}),
	}
	md := Markdown(evs, Options{SessionID: "s1"})
	// The idle body echoes the model turn, so the report text appears once inline
	// (from the model_turn) plus once in the Final report section.
	if !strings.Contains(md, "## Final report") {
		t.Fatalf("expected final report section:\n%s", md)
	}
	if c := countSub(md, report); c != 2 {
		t.Fatalf("report should appear twice (model turn + final report), got %d:\n%s", c, md)
	}
}

func TestCommitAndReview(t *testing.T) {
	evs := []*v1.Event{
		ev(1, "coordinator", "commit_made", map[string]any{"sha": "abcdef1234567890", "message": "add feature"}),
		ev(2, "reviewer:gpt", "review_submitted", map[string]any{"model": "gpt", "verdict": "accept", "summary": "looks good"}),
	}
	md := Markdown(evs, Options{SessionID: "s1"})
	if !strings.Contains(md, "commit `abcdef123456`") {
		t.Fatalf("expected commit line with short sha:\n%s", md)
	}
	if !strings.Contains(md, "add feature") {
		t.Fatalf("expected commit message:\n%s", md)
	}
	if !strings.Contains(md, "**ACCEPT**") {
		t.Fatalf("expected uppercased verdict:\n%s", md)
	}
	if !strings.Contains(md, "looks good") {
		t.Fatalf("expected review summary:\n%s", md)
	}
}

func TestUsageFooterFromEvents(t *testing.T) {
	evs := []*v1.Event{
		ev(1, "coordinator", "model_turn", map[string]any{
			"text": "hi", "model_name": "m1",
			"usage": map[string]any{"input": float64(100), "output": float64(50), "total": float64(150)},
		}),
		ev(2, "coordinator", "model_turn", map[string]any{
			"text": "there", "model_name": "m1",
			"usage": map[string]any{"input": float64(10), "output": float64(5), "total": float64(15)},
		}),
	}
	md := Markdown(evs, Options{SessionID: "s1"})
	if !strings.Contains(md, "## Usage") {
		t.Fatalf("expected usage section:\n%s", md)
	}
	if !strings.Contains(md, "| m1 | 110 | 55 |") {
		t.Fatalf("expected summed per-model row:\n%s", md)
	}
	if !strings.Contains(md, "**TOTAL**") {
		t.Fatalf("expected total row:\n%s", md)
	}
	// no cost column when no usage rows supplied
	if strings.Contains(md, "Cost") {
		t.Fatalf("no cost column expected without usage rows:\n%s", md)
	}
}

func TestUsageFooterWithCost(t *testing.T) {
	evs := []*v1.Event{
		ev(1, "coordinator", "model_turn", map[string]any{"text": "hi", "model_name": "m1"}),
	}
	rows := []*v1.UsageRow{
		{Session: "s1", Model: "m1", Input: 100, Output: 50, Total: 150, Cost: 0.1234, PriceStatus: "priced"},
	}
	md := Markdown(evs, Options{SessionID: "s1", Usage: rows})
	if !strings.Contains(md, "| Model | Input | Output | Cache | Total | Cost |") {
		t.Fatalf("expected cost column header:\n%s", md)
	}
	if !strings.Contains(md, "$0.1234") {
		t.Fatalf("expected cost cell:\n%s", md)
	}
}

func TestUserInputQueued(t *testing.T) {
	evs := []*v1.Event{
		ev(1, "user", "user_input", map[string]any{"text": "do the thing", "queued": true}),
	}
	md := Markdown(evs, Options{SessionID: "s1"})
	if !strings.Contains(md, "**user:**") {
		t.Fatalf("expected user prefix:\n%s", md)
	}
	if !strings.Contains(md, "(queued, undelivered)") {
		t.Fatalf("expected queued marker:\n%s", md)
	}
	if !strings.Contains(md, "do the thing") {
		t.Fatalf("expected user text:\n%s", md)
	}

	// once delivered, the marker disappears
	evs2 := append(evs, ev(2, "system", "user_input_delivered", map[string]any{"seq": float64(1)}))
	md2 := Markdown(evs2, Options{SessionID: "s1"})
	if strings.Contains(md2, "(queued, undelivered)") {
		t.Fatalf("delivered input should not be marked queued:\n%s", md2)
	}
}

func TestHeaderAndMetadata(t *testing.T) {
	evs := []*v1.Event{
		{Seq: 1, Actor: "system", Type: "session_started", Ts: "2026-07-06T12:00:00Z",
			DataJson: `{"mode":"work","workspace":"/tmp/proj","interaction_level":"autonomous"}`},
	}
	md := Markdown(evs, Options{SessionID: "s_abc"})
	if !strings.HasPrefix(md, "# Session s_abc") {
		t.Fatalf("expected header:\n%s", md)
	}
	for _, want := range []string{"mode: work", "workspace: /tmp/proj", "level: autonomous", "started: 2026-07-06"} {
		if !strings.Contains(md, want) {
			t.Fatalf("expected metadata %q:\n%s", want, md)
		}
	}
}

func TestTransientSkipped(t *testing.T) {
	evs := []*v1.Event{
		{Seq: 0, Actor: "coordinator", Type: "model_turn", DataJson: `{"text":"transient"}`, Transient: true},
		ev(1, "coordinator", "model_turn", map[string]any{"text": "persisted"}),
	}
	md := Markdown(evs, Options{SessionID: "s1"})
	if strings.Contains(md, "transient") {
		t.Fatalf("transient events should be skipped:\n%s", md)
	}
	if !strings.Contains(md, "persisted") {
		t.Fatalf("expected persisted text:\n%s", md)
	}
}
