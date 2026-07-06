package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
)

// fakeSteer returns a scripted set of corrections, one slice per Checkpoint call.
type fakeSteer struct {
	msgs  [][]string
	calls int
}

func (f *fakeSteer) Checkpoint(ctx context.Context) ([]string, error) {
	defer func() { f.calls++ }()
	if f.calls < len(f.msgs) {
		return f.msgs[f.calls], nil
	}
	return nil, nil
}

// A correction returned at the first checkpoint is appended to the conversation
// the model sees on a later turn (spec §18.7).
func TestLoopSteerInjectsCorrection(t *testing.T) {
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{
		assistantText("done"),
	}}
	loop := newLoop(t, turner)
	loop.Seed("do the thing")
	loop.Steer = &fakeSteer{msgs: [][]string{{"actually do the other thing"}}}

	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The first turn's request should include both the seed and the steered
	// correction as user messages, in order.
	var users []string
	for _, mm := range turner.lastMsgs {
		if mm.Role == "user" {
			users = append(users, mm.Content)
		}
	}
	if len(users) < 2 {
		t.Fatalf("want >=2 user messages, got %v", users)
	}
	joined := strings.Join(users, "|")
	if !strings.Contains(joined, "actually do the other thing") {
		t.Fatalf("correction not seen by model: %v", users)
	}
	if users[0] != "do the thing" || users[1] != "actually do the other thing" {
		t.Fatalf("messages out of order: %v", users)
	}
}

// With Steer == nil the checkpoint is a no-op: behavior is unchanged.
func TestLoopSteerNilNoop(t *testing.T) {
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{
		assistantText("ok"),
	}}
	loop := newLoop(t, turner)
	loop.Seed("hi")
	if loop.Steer != nil {
		t.Fatal("Steer should be nil")
	}
	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Report != "ok" {
		t.Fatalf("report = %q", res.Report)
	}
}

// A checkpoint message arriving MID-BATCH — after the first tool result of a
// multi-tool-call turn (e.g. a finished-job notification or steered
// correction) — must NOT be posted between that turn's tool results: Anthropic
// requires every tool_result to sit immediately after its tool_use message and
// rejects the next request with "tool_use ids were found without tool_result
// blocks immediately after" otherwise. The message is deferred to the end of
// the batch.
func TestLoopSteerMidBatchDeferredToBatchEnd(t *testing.T) {
	twoCalls := &gollama.ResponseMessageGenerate{Choices: []gollama.GenChoice{{Message: gollama.Message{
		Role: "assistant",
		ToolCalls: []gollama.ToolCall{
			{ID: "c1", Type: "function", Function: gollama.ToolCallFunction{Name: "read_file", Arguments: `{}`}},
			{ID: "c2", Type: "function", Function: gollama.ToolCallFunction{Name: "read_file", Arguments: `{}`}},
		},
	}}}}
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{twoCalls, assistantText("done")}}
	loop := newLoop(t, turner)
	loop.Seed("start")
	// Checkpoint call order: top of turn 1 (call 0), after c1's result (call 1,
	// MID-BATCH), after c2's result (call 2), top of turn 2 (call 3). The note
	// arrives at the mid-batch checkpoint.
	loop.Steer = &fakeSteer{msgs: [][]string{nil, {"[job job_1 done] build ok"}}}

	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	h := loop.History()
	var shape []string
	for _, m := range h {
		s := m.Role
		if m.Role == "tool" {
			s += ":" + m.ToolCallID
		}
		shape = append(shape, s)
	}
	want := []string{"user", "assistant", "tool:c1", "tool:c2", "user", "assistant"}
	if strings.Join(shape, ",") != strings.Join(want, ",") {
		t.Fatalf("history shape = %v, want %v", shape, want)
	}
	if h[4].Content != "[job job_1 done] build ok" {
		t.Fatalf("deferred note = %q, want the job notification", h[4].Content)
	}
}
