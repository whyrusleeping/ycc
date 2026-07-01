package engine

import (
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
)

// A queued mid-run echo (queued:true) is NOT appended at its echo position; the
// matching user_input_delivered event appends exactly one user message at the
// real delivery point (after a tool_result, before the next turn) — reproducing
// what the model saw live (spec §18.7).
func TestReplaySteerByDefaultDelivered(t *testing.T) {
	events := []event.Event{
		{Seq: 1, Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "start"}},
		{Seq: 2, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "working"}},
		{Seq: 3, Actor: "coordinator", Type: event.ToolCall, Data: map[string]any{"name": "read", "args": "{}", "id": "c1"}},
		// Queued mid-run echo: must be skipped at this position.
		{Seq: 4, Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "no, wrong file", "queued": true}},
		{Seq: 5, Actor: "coordinator", Type: event.ToolResult, Data: map[string]any{"id": "c1", "result": "ok"}},
		// Delivered at the checkpoint after the tool result: appended here.
		{Seq: 6, Actor: "user", Type: event.UserInputDelivered, Data: map[string]any{"seq": 4, "text": "no, wrong file"}},
		{Seq: 7, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "fixing"}},
	}

	got := ReplayHistory(events)
	want := []gollama.Message{
		{Role: "user", Content: "start"},
		{
			Role:    "assistant",
			Content: "working",
			ToolCalls: []gollama.ToolCall{
				{ID: "c1", Type: "function", Function: gollama.ToolCallFunction{Name: "read", Arguments: "{}"}},
			},
		},
		{Role: "tool", ToolCallID: "c1", Content: "ok"},
		{Role: "user", Content: "no, wrong file"},
		{Role: "assistant", Content: "fixing"},
	}
	if len(got) != len(want) {
		t.Fatalf("history len = %d, want %d:\n%+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Content != want[i].Content {
			t.Fatalf("msg[%d] = {%s %q}, want {%s %q}", i, got[i].Role, got[i].Content, want[i].Role, want[i].Content)
		}
	}
	// Exactly one occurrence of the steered text (not doubled by the echo).
	n := 0
	for _, m := range got {
		if m.Content == "no, wrong file" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("steered text appears %d times, want 1", n)
	}
}

// A queued echo with NO matching delivered event (session stopped mid-run) is
// omitted from replayed history — it never reached the model.
func TestReplayQueuedWithoutDeliveryOmitted(t *testing.T) {
	events := []event.Event{
		{Seq: 1, Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "start"}},
		{Seq: 2, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "working"}},
		{Seq: 3, Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "never delivered", "queued": true}},
	}
	got := ReplayHistory(events)
	for _, m := range got {
		if m.Content == "never delivered" {
			t.Fatalf("queued-but-undelivered input leaked into history: %+v", got)
		}
	}
	if len(got) != 2 {
		t.Fatalf("history len = %d, want 2: %+v", len(got), got)
	}
}
