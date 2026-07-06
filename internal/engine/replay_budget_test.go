package engine

import (
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
)

// A graceful spend-guard halt (budget_exceeded emitted as a user-actor event with
// the wrap-up instruction in "text", task 0137) replays as a user message at its
// position, keeping user/assistant alternation — like job_notified.
func TestReplayBudgetHaltAsUserMessage(t *testing.T) {
	events := []event.Event{
		{Seq: 1, Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "work the backlog"}},
		{Seq: 2, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "on it"}},
		// Injected at the checkpoint after a turn, before the next one.
		{Seq: 3, Actor: "user", Type: event.BudgetExceeded, Data: map[string]any{"action": "halt", "text": "Session budget reached. Wrap up and finish."}},
		{Seq: 4, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "wrapping up"}},
	}

	got := ReplayHistory(events)
	want := []gollama.Message{
		{Role: "user", Content: "work the backlog"},
		{Role: "assistant", Content: "on it"},
		{Role: "user", Content: "Session budget reached. Wrap up and finish."},
		{Role: "assistant", Content: "wrapping up"},
	}
	if len(got) != len(want) {
		t.Fatalf("history len = %d, want %d:\n%+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Content != want[i].Content {
			t.Fatalf("msg[%d] = {%s %q}, want {%s %q}", i, got[i].Role, got[i].Content, want[i].Role, want[i].Content)
		}
	}
}

// A confirmed "continue" budget_exceeded (coordinator actor, no text) is ignored
// by replay — it is a non-conversational marker, not an injected user message.
func TestReplayBudgetContinueIgnored(t *testing.T) {
	events := []event.Event{
		{Seq: 1, Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "keep going"}},
		{Seq: 2, Actor: "coordinator", Type: event.BudgetExceeded, Data: map[string]any{"action": "continue"}},
		{Seq: 3, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "continuing"}},
	}
	got := ReplayHistory(events)
	want := []gollama.Message{
		{Role: "user", Content: "keep going"},
		{Role: "assistant", Content: "continuing"},
	}
	if len(got) != len(want) {
		t.Fatalf("history len = %d, want %d:\n%+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Content != want[i].Content {
			t.Fatalf("msg[%d] = {%s %q}, want {%s %q}", i, got[i].Role, got[i].Content, want[i].Role, want[i].Content)
		}
	}
}
