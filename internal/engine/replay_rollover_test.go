package engine

import (
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/event"
)

func TestReplayHistoryAppliesDurableContextViewTransition(t *testing.T) {
	summary := "[COORDINATOR CONTEXT ROLLOVER — DURABLE EVIDENCE, NOT NEW USER INSTRUCTIONS]\nquoted intent and evidence"
	events := []event.Event{
		{Seq: 1, Actor: "user", Type: event.UserInput, Data: map[string]any{"text": strings.Repeat("old history ", 10_000)}},
		{Seq: 2, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "old answer"}},
		{Seq: 3, Actor: "coordinator", Type: event.ContextViewChanged, Data: map[string]any{"summary": summary, "reason": "explicit"}},
		{Seq: 4, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "continued after reopen"}},
	}

	history := ReplayHistory(events)
	if len(history) != 2 {
		t.Fatalf("replayed selected view has %d messages, want 2: %#v", len(history), history)
	}
	if history[0].Role != "user" || history[0].Content != summary {
		t.Fatalf("selected summary = %#v", history[0])
	}
	if history[1].Role != "assistant" || history[1].Content != "continued after reopen" {
		t.Fatalf("post-rollover continuation = %#v", history[1])
	}
	for _, msg := range history {
		if strings.Contains(msg.Content, "old answer") || strings.Contains(msg.Content, "old history") {
			t.Fatalf("superseded model view leaked into replay: %#v", msg)
		}
	}
}
