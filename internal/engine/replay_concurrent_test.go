package engine

import (
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
)

// When the coordinator runs subagents as BACKGROUND jobs, their actor-tagged
// events interleave concurrently with the coordinator's own turns and tool
// results in the single ordered log — an implementer or reviewer event can land
// BETWEEN a coordinator tool_call and its matching tool_result, and between
// coordinator turns. ReplayHistory must still reconstruct a valid coordinator-only
// history: subagent events excluded, every coordinator tool_use answered exactly
// once, the job_notified report injected as a user message at its recorded
// position, and an unfinished agent job producing a lost-job note.
func TestReplayHistoryInterleavedConcurrentSubagents(t *testing.T) {
	events := []event.Event{
		{Seq: 1, Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "do task 0042"}},
		// Coordinator spawns two background jobs (an implementer and a reviewer set).
		{Seq: 2, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "spawning background workers"}},
		{Seq: 3, Actor: "coordinator", Type: event.ToolCall, Data: map[string]any{"name": "spawn_implementer", "args": `{"background":true}`, "id": "c1"}},
		// Concurrent implementer activity lands BETWEEN the coordinator tool_call
		// and its tool_result — must be filtered out and must not disturb pairing.
		{Seq: 4, Actor: "implementer", Type: event.ModelTurn, Data: map[string]any{"text": "reading files"}},
		{Seq: 5, Actor: "implementer", Type: event.ToolCall, Data: map[string]any{"name": "Read", "args": `{}`, "id": "i1"}},
		{Seq: 6, Actor: "coordinator", Type: event.JobStarted, Data: map[string]any{"id": "job_1", "kind": "agent", "label": "implementer 0042"}},
		{Seq: 7, Actor: "coordinator", Type: event.ToolResult, Data: map[string]any{"id": "c1", "result": "started background job job_1"}},
		{Seq: 8, Actor: "implementer", Type: event.ToolResult, Data: map[string]any{"id": "i1", "result": "file contents"}},
		{Seq: 9, Actor: "coordinator", Type: event.ToolCall, Data: map[string]any{"name": "spawn_reviewers", "args": `{"background":true}`, "id": "c2"}},
		// Reviewer activity interleaves before the reviewers' tool_result.
		{Seq: 10, Actor: "reviewer:opus", Type: event.ModelTurn, Data: map[string]any{"text": "inspecting diff"}},
		{Seq: 11, Actor: "coordinator", Type: event.JobStarted, Data: map[string]any{"id": "job_2", "kind": "agent", "label": "reviewers 0042"}},
		{Seq: 12, Actor: "coordinator", Type: event.ToolResult, Data: map[string]any{"id": "c2", "result": "started background job job_2"}},
		{Seq: 13, Actor: "reviewer:opus", Type: event.ToolCall, Data: map[string]any{"name": "submit_review", "args": `{}`, "id": "r1"}},
		{Seq: 14, Actor: "reviewer:opus", Type: event.ToolResult, Data: map[string]any{"id": "r1", "result": "ok"}},
		// The implementer job finishes; its report is injected at a checkpoint.
		{Seq: 15, Actor: "implementer", Type: event.ModelTurn, Data: map[string]any{"text": "done editing"}},
		{Seq: 16, Actor: "coordinator", Type: event.JobFinished, Data: map[string]any{"id": "job_1", "status": "done", "tail": "IMPLEMENTER REPORT: did it"}},
		{Seq: 17, Actor: "user", Type: event.JobNotified, Data: map[string]any{"id": "job_1", "text": "[job job_1 done] implementer 0042\nIMPLEMENTER REPORT: did it"}},
		// Coordinator continues; the reviewers job (job_2) never finished before the
		// log ended (daemon restart) → a lost-job note is synthesized.
		{Seq: 18, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "implementer done; waiting on reviewers"}},
	}

	got := ReplayHistory(events)
	want := []gollama.Message{
		{Role: "user", Content: "do task 0042"},
		{Role: "assistant", Content: "spawning background workers", ToolCalls: []gollama.ToolCall{
			{ID: "c1", Type: "function", Function: gollama.ToolCallFunction{Name: "spawn_implementer", Arguments: `{"background":true}`}},
			{ID: "c2", Type: "function", Function: gollama.ToolCallFunction{Name: "spawn_reviewers", Arguments: `{"background":true}`}},
		}},
		{Role: "tool", ToolCallID: "c1", Content: "started background job job_1"},
		{Role: "tool", ToolCallID: "c2", Content: "started background job job_2"},
		{Role: "user", Content: "[job job_1 done] implementer 0042\nIMPLEMENTER REPORT: did it"},
		{Role: "assistant", Content: "implementer done; waiting on reviewers"},
		// job_2 started but never finished → synthesized lost-job note (a fresh user
		// message, since the trailing message is an assistant turn).
		{Role: "user", Content: "[job job_2 lost: daemon restarted] reviewers 0042"},
	}

	if len(got) != len(want) {
		t.Fatalf("history len = %d, want %d:\n%+v", len(got), len(want), got)
	}
	for i := range want {
		g := got[i]
		if g.Role != want[i].Role || g.Content != want[i].Content {
			t.Fatalf("msg[%d] = {%s %q}, want {%s %q}", i, g.Role, g.Content, want[i].Role, want[i].Content)
		}
		if len(g.ToolCalls) != len(want[i].ToolCalls) {
			t.Fatalf("msg[%d] tool calls = %d, want %d", i, len(g.ToolCalls), len(want[i].ToolCalls))
		}
		for j := range want[i].ToolCalls {
			if g.ToolCalls[j].ID != want[i].ToolCalls[j].ID || g.ToolCalls[j].Function.Name != want[i].ToolCalls[j].Function.Name {
				t.Fatalf("msg[%d] toolcall[%d] = %+v, want %+v", i, j, g.ToolCalls[j], want[i].ToolCalls[j])
			}
		}
	}

	// No subagent (implementer/reviewer) content leaked into the coordinator history.
	for _, m := range got {
		for _, bad := range []string{"reading files", "inspecting diff", "done editing", "file contents"} {
			if strings.Contains(m.Content, bad) {
				t.Fatalf("subagent content leaked into coordinator history: %q", m.Content)
			}
		}
	}
}
