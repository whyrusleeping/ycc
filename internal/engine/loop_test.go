package engine

import (
	"context"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/tools"
)

// scriptedTurner returns a pre-programmed sequence of responses, one per Turn
// call, recording the requests it saw so the test can assert on context growth.
type scriptedTurner struct {
	responses []*gollama.ResponseMessageGenerate
	calls     int
	lastMsgs  []gollama.Message
}

func (s *scriptedTurner) Turn(opts gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	s.lastMsgs = opts.Messages
	r := s.responses[s.calls]
	s.calls++
	return r, nil
}

func assistantToolCall(name, args string) *gollama.ResponseMessageGenerate {
	return &gollama.ResponseMessageGenerate{Choices: []gollama.GenChoice{{Message: gollama.Message{
		Role:      "assistant",
		ToolCalls: []gollama.ToolCall{{ID: "c1", Type: "function", Function: gollama.ToolCallFunction{Name: name, Arguments: args}}},
	}}}}
}

func assistantText(text string) *gollama.ResponseMessageGenerate {
	return &gollama.ResponseMessageGenerate{Choices: []gollama.GenChoice{{Message: gollama.Message{Role: "assistant", Content: text}}}}
}

func newLoop(t *testing.T, turner Turner) *Loop {
	t.Helper()
	reg := tools.New()
	reg.Add(tools.Worker(&tools.Workspace{Root: t.TempDir()})...)
	return &Loop{
		Client:  turner,
		Model:   "test",
		Tools:   reg,
		Emitter: event.NewEmitter(nil, "agent"),
	}
}

// A control tool (finish) ends the loop and surfaces its report.
func TestLoopStopsOnFinish(t *testing.T) {
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{
		assistantToolCall("finish", `{"report":"all done"}`),
	}}
	res, err := newLoop(t, turner).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Report != "all done" {
		t.Fatalf("report = %q, want %q", res.Report, "all done")
	}
	if res.Turns != 1 {
		t.Fatalf("turns = %d, want 1", res.Turns)
	}
}

// A turn with no tool calls yields, returning the assistant text as the report.
func TestLoopYieldsOnNoToolCalls(t *testing.T) {
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{
		assistantText("nothing left to do"),
	}}
	res, err := newLoop(t, turner).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Report != "nothing left to do" {
		t.Fatalf("report = %q", res.Report)
	}
}

// Tool results are fed back into context, and the loop continues across turns
// until a control tool stops it.
func TestLoopFeedsResultsAndContinues(t *testing.T) {
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{
		assistantToolCall("write_file", `{"path":"a.txt","content":"hi"}`),
		assistantToolCall("finish", `{"report":"wrote a.txt"}`),
	}}
	loop := newLoop(t, turner)
	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Turns != 2 {
		t.Fatalf("turns = %d, want 2", res.Turns)
	}
	// By the 2nd turn the history must contain: user seed is absent (none seeded),
	// assistant tool_call, and the tool result message.
	var sawToolResult bool
	for _, m := range turner.lastMsgs {
		if m.Role == "tool" && m.ToolCallID == "c1" {
			sawToolResult = true
		}
	}
	if !sawToolResult {
		t.Fatal("tool result was not fed back into context")
	}
}

// The loop terminates with an error when it exceeds MaxTurns (model never stops).
func TestLoopMaxTurns(t *testing.T) {
	loopForever := make([]*gollama.ResponseMessageGenerate, 10)
	for i := range loopForever {
		loopForever[i] = assistantToolCall("list_dir", `{"path":"."}`)
	}
	turner := &scriptedTurner{responses: loopForever}
	loop := newLoop(t, turner)
	loop.MaxTurns = 3
	_, err := loop.Run(context.Background())
	if err == nil {
		t.Fatal("expected max-turns error, got nil")
	}
}
