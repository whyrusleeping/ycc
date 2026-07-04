package engine

import (
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
)

// A checkpoint-injected job notification (job_notified, recorded as a user-actor
// event) replays as a user message at its position, keeping user/assistant
// alternation — the same rule as a steer correction.
func TestReplayJobNotifiedAsUserMessage(t *testing.T) {
	events := []event.Event{
		{Seq: 1, Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "build it"}},
		{Seq: 2, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "starting build"}},
		{Seq: 3, Actor: "coordinator", Type: event.ToolCall, Data: map[string]any{"name": "Bash", "args": "{}", "id": "c1"}},
		{Seq: 4, Actor: "coordinator", Type: event.ToolResult, Data: map[string]any{"id": "c1", "result": "started job_1"}},
		{Seq: 5, Actor: "coordinator", Type: event.JobStarted, Data: map[string]any{"id": "job_1", "kind": "bash", "label": "go build ./..."}},
		{Seq: 6, Actor: "coordinator", Type: event.JobFinished, Data: map[string]any{"id": "job_1", "status": "done", "tail": "exit 0"}},
		// Injected at the checkpoint after the tool result, before the next turn.
		{Seq: 7, Actor: "user", Type: event.JobNotified, Data: map[string]any{"id": "job_1", "text": "[job job_1 done] go build ./...\nexit 0"}},
		{Seq: 8, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "build passed"}},
	}

	got := ReplayHistory(events)
	want := []gollama.Message{
		{Role: "user", Content: "build it"},
		{Role: "assistant", Content: "starting build", ToolCalls: []gollama.ToolCall{{ID: "c1", Type: "function", Function: gollama.ToolCallFunction{Name: "Bash", Arguments: "{}"}}}},
		{Role: "tool", ToolCallID: "c1", Content: "started job_1"},
		{Role: "user", Content: "[job job_1 done] go build ./...\nexit 0"},
		{Role: "assistant", Content: "build passed"},
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

// A job that started but whose finish was never recorded (daemon restart mid
// flight) gets a synthesized "(job lost)" note so histories stay valid.
func TestReplayLostJobSynthesized(t *testing.T) {
	events := []event.Event{
		{Seq: 1, Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "run tests"}},
		{Seq: 2, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "kicking off"}},
		{Seq: 3, Actor: "coordinator", Type: event.JobStarted, Data: map[string]any{"id": "job_1", "kind": "bash", "label": "go test ./..."}},
		// No job_finished / job_notified: the daemon died with job_1 in flight.
	}
	got := ReplayHistory(events)
	last := got[len(got)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "job_1 lost") {
		t.Fatalf("last message = {%s %q}, want a user lost-job note", last.Role, last.Content)
	}
	if !strings.Contains(last.Content, "go test ./...") {
		t.Fatalf("lost-job note missing label: %q", last.Content)
	}
}

// A finished job (job_finished recorded, e.g. consumed by wait) is NOT treated
// as lost — no synthesized note.
func TestReplayFinishedJobNotLost(t *testing.T) {
	events := []event.Event{
		{Seq: 1, Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "run tests"}},
		{Seq: 2, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "kicking off"}},
		{Seq: 3, Actor: "coordinator", Type: event.JobStarted, Data: map[string]any{"id": "job_1", "kind": "bash", "label": "go test ./..."}},
		{Seq: 4, Actor: "coordinator", Type: event.JobFinished, Data: map[string]any{"id": "job_1", "status": "done", "tail": "exit 0"}},
	}
	got := ReplayHistory(events)
	for _, m := range got {
		if strings.Contains(m.Content, "lost") {
			t.Fatalf("finished job wrongly synthesized a lost note: %+v", got)
		}
	}
}
