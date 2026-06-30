package engine

import (
	"regexp"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
)

// coordinatorSession builds a representative event sequence: a user input; a
// coordinator model_turn with thinking blocks (incl. a signed one) and text; a
// coordinator tool_call and its tool_result; an interleaved SUBAGENT
// (implementer) model_turn + tool_call that must be filtered out; and a final
// coordinator model_turn that yields (no tool calls).
func coordinatorSession() []event.Event {
	return []event.Event{
		{Seq: 1, Type: event.SessionStarted, Data: map[string]any{"mode": "work"}},
		{Seq: 2, Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "do the thing"}},
		{Seq: 3, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{
			"text": "I'll start by reading.",
			"thinking_blocks": []event.ThinkingBlock{
				{Thinking: "let me think", Signature: "sig-abc"},
				{Redacted: "opaque-data"},
			},
		}},
		{Seq: 4, Actor: "coordinator", Type: event.ToolCall, Data: map[string]any{
			"name": "read_file", "args": `{"path":"x"}`, "id": "call_1",
		}},
		{Seq: 5, Actor: "coordinator", Type: event.ToolResult, Data: map[string]any{
			"id": "call_1", "result": "file contents",
		}},
		// Interleaved subagent activity — must be ignored by ReplayHistory.
		{Seq: 6, Actor: "implementer", Type: event.ModelTurn, Data: map[string]any{"text": "subagent thinking"}},
		{Seq: 7, Actor: "implementer", Type: event.ToolCall, Data: map[string]any{
			"name": "write_file", "args": `{}`, "id": "sub_1",
		}},
		{Seq: 8, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "All done."}},
		{Seq: 9, Type: event.SessionIdle, Data: map[string]any{"report": "All done."}},
	}
}

func wantHistory() []gollama.Message {
	return []gollama.Message{
		{Role: "user", Content: "do the thing"},
		{
			Role:    "assistant",
			Content: "I'll start by reading.",
			ThinkingBlocks: []gollama.ThinkingBlock{
				{Thinking: "let me think", Signature: "sig-abc"},
				{Redacted: "opaque-data"},
			},
			ToolCalls: []gollama.ToolCall{
				{ID: "call_1", Type: "function", Function: gollama.ToolCallFunction{Name: "read_file", Arguments: `{"path":"x"}`}},
			},
		},
		{Role: "tool", ToolCallID: "call_1", Content: "file contents"},
		{Role: "assistant", Content: "All done."},
	}
}

func TestReplayHistoryTyped(t *testing.T) {
	got := ReplayHistory(coordinatorSession())
	want := wantHistory()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReplayHistory mismatch:\n got=%+v\nwant=%+v", got, want)
	}
}

// TestReplayHistoryFromDisk round-trips each event through JSON (so Data becomes
// map[string]any, as it would when read back from events.jsonl) and asserts the
// decoded-map path reconstructs identically.
func TestReplayHistoryFromDisk(t *testing.T) {
	src := coordinatorSession()
	decoded := make([]event.Event, len(src))
	for i, ev := range src {
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := json.Unmarshal(b, &decoded[i]); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
	}
	got := ReplayHistory(decoded)
	want := wantHistory()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReplayHistory (from disk) mismatch:\n got=%+v\nwant=%+v", got, want)
	}
}

// TestReplayHistoryDanglingToolCall covers a session reopened mid-turn: the last
// assistant message has a tool_call with no following tool_result, so a synthetic
// tool message is appended to keep the conversation valid for the next turn.
func TestReplayHistoryDanglingToolCall(t *testing.T) {
	events := []event.Event{
		{Seq: 1, Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "go"}},
		{Seq: 2, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "working"}},
		{Seq: 3, Actor: "coordinator", Type: event.ToolCall, Data: map[string]any{
			"name": "read_file", "args": `{}`, "id": "dangling",
		}},
	}
	got := ReplayHistory(events)
	if len(got) != 3 {
		t.Fatalf("want 3 messages, got %d: %+v", len(got), got)
	}
	last := got[2]
	if last.Role != "tool" || last.ToolCallID != "dangling" {
		t.Fatalf("want synthetic tool result for dangling call, got %+v", last)
	}
	if last.Content == "" {
		t.Fatalf("synthetic tool result should have non-empty content")
	}
}

// TestReplayHistoryCanonicalizesToolIDs covers reopening a session whose recorded
// tool-call ids are not valid Anthropic tool_use ids (e.g. they came from a
// different backend and contain '.'/':' or are empty). Both the assistant
// tool_use id and the matching tool_result tool_use_id must be rewritten to the
// SAME canonical, pattern-valid id so the conversation is accepted on resume.
func TestReplayHistoryCanonicalizesToolIDs(t *testing.T) {
	valid := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	events := []event.Event{
		{Seq: 1, Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "go"}},
		{Seq: 2, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "working"}},
		{Seq: 3, Actor: "coordinator", Type: event.ToolCall, Data: map[string]any{
			"name": "read_file", "args": `{}`, "id": "call_abc.0:xyz",
		}},
		{Seq: 4, Actor: "coordinator", Type: event.ToolResult, Data: map[string]any{
			"name": "read_file", "result": "ok", "id": "call_abc.0:xyz",
		}},
		// A second tool call with an EMPTY id (some backends omit it).
		{Seq: 5, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "again"}},
		{Seq: 6, Actor: "coordinator", Type: event.ToolCall, Data: map[string]any{
			"name": "read_file", "args": `{}`, "id": "",
		}},
		{Seq: 7, Actor: "coordinator", Type: event.ToolResult, Data: map[string]any{
			"name": "read_file", "result": "ok2", "id": "",
		}},
	}
	got := ReplayHistory(events)

	// Collect each assistant tool_use id and each tool_result id, asserting all
	// are pattern-valid and that each result matches its call.
	var toolUseIDs []string
	resultByID := map[string]bool{}
	for _, m := range got {
		for _, c := range m.ToolCalls {
			if !valid.MatchString(c.ID) {
				t.Fatalf("tool_use id %q does not match Anthropic pattern", c.ID)
			}
			toolUseIDs = append(toolUseIDs, c.ID)
		}
		if m.Role == "tool" {
			if !valid.MatchString(m.ToolCallID) {
				t.Fatalf("tool_result tool_use_id %q does not match Anthropic pattern", m.ToolCallID)
			}
			resultByID[m.ToolCallID] = true
		}
	}
	if len(toolUseIDs) != 2 {
		t.Fatalf("want 2 tool_use ids, got %d", len(toolUseIDs))
	}
	for _, id := range toolUseIDs {
		if !resultByID[id] {
			t.Fatalf("tool_use id %q has no matching tool_result", id)
		}
	}
}

// TestReplayHistoryLegacyMissingResultID reproduces the real on-disk case: logs
// written before the loop recorded an "id" on tool_result events have a valid
// toolu_… id on the tool_call but NO id on the matching tool_result. Replay must
// recover the pairing positionally (each result follows its call) so the
// tool_result carries the call's real id rather than an empty tool_use_id (which
// Anthropic rejects: tool_use_id must match ^[a-zA-Z0-9_-]+$).
func TestReplayHistoryLegacyMissingResultID(t *testing.T) {
	events := []event.Event{
		{Seq: 1, Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "go"}},
		{Seq: 2, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "t"}},
		{Seq: 3, Actor: "coordinator", Type: event.ToolCall, Data: map[string]any{
			"name": "Read", "args": `{}`, "id": "toolu_01AAAA",
		}},
		// Legacy tool_result: no "id" field at all.
		{Seq: 4, Actor: "coordinator", Type: event.ToolResult, Data: map[string]any{
			"name": "Read", "result": "first",
		}},
		{Seq: 5, Actor: "coordinator", Type: event.ToolCall, Data: map[string]any{
			"name": "Read", "args": `{}`, "id": "toolu_01BBBB",
		}},
		{Seq: 6, Actor: "coordinator", Type: event.ToolResult, Data: map[string]any{
			"name": "Read", "result": "second",
		}},
	}
	got := ReplayHistory(events)

	// Map each tool result back to the call it answers, by id, and check the
	// content lines up with the FIFO order (first result -> first call).
	var calls []gollama.ToolCall
	results := map[string]string{} // tool_use_id -> result content
	for _, m := range got {
		calls = append(calls, m.ToolCalls...)
		if m.Role == "tool" {
			if m.ToolCallID == "" {
				t.Fatal("tool_result has empty tool_use_id; Anthropic would 400")
			}
			results[m.ToolCallID] = m.Content
		}
	}
	if len(calls) != 2 {
		t.Fatalf("want 2 tool calls, got %d", len(calls))
	}
	if results[calls[0].ID] != "first" {
		t.Fatalf("call %q paired to %q, want first", calls[0].ID, results[calls[0].ID])
	}
	if results[calls[1].ID] != "second" {
		t.Fatalf("call %q paired to %q, want second", calls[1].ID, results[calls[1].ID])
	}
	// The real recorded ids are valid and must be preserved as-is.
	if calls[0].ID != "toolu_01AAAA" || calls[1].ID != "toolu_01BBBB" {
		t.Fatalf("valid recorded ids were not preserved: %q, %q", calls[0].ID, calls[1].ID)
	}
}

// TestReplayHistoryTruncatedDropsThinking: a coordinator model_turn marked
// truncated may carry an unsigned/cut-off thinking block, so ReplayHistory drops
// the blocks to match the live loop's sanitized stub. Covers both the typed and
// JSON-decoded (bool) shapes.
func TestReplayHistoryTruncatedDropsThinking(t *testing.T) {
	mk := func() []event.Event {
		return []event.Event{
			{Seq: 1, Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "go"}},
			{Seq: 2, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{
				"text":      "cut off mid-thought",
				"truncated": true,
				"thinking_blocks": []event.ThinkingBlock{
					{Thinking: "incomplete", Signature: ""},
				},
			}},
		}
	}

	check := func(t *testing.T, events []event.Event) {
		got := ReplayHistory(events)
		if len(got) != 2 {
			t.Fatalf("want 2 messages, got %d: %+v", len(got), got)
		}
		if got[1].Role != "assistant" || got[1].Content != "cut off mid-thought" {
			t.Fatalf("unexpected assistant message: %+v", got[1])
		}
		if got[1].ThinkingBlocks != nil {
			t.Fatalf("truncated turn should drop thinking blocks, got %+v", got[1].ThinkingBlocks)
		}
	}

	t.Run("typed", func(t *testing.T) { check(t, mk()) })
	t.Run("from_disk", func(t *testing.T) {
		src := mk()
		decoded := make([]event.Event, len(src))
		for i, ev := range src {
			b, err := json.Marshal(ev)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if err := json.Unmarshal(b, &decoded[i]); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
		}
		check(t, decoded)
	})
}

// TestReplayHistoryTruncationBoundary covers reconstruction across a mid-Run
// truncation-retry boundary: a truncated coordinator turn (empty text + unsigned
// thinking block) immediately followed by the retry turn. The live loop posts an
// internal user "nudge" between them that is NOT recorded in the event log, so
// ReplayHistory must synthesize it to preserve strict user/assistant alternation.
func TestReplayHistoryTruncationBoundary(t *testing.T) {
	mk := func() []event.Event {
		return []event.Event{
			{Seq: 1, Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "go"}},
			{Seq: 2, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{
				"text":      "   ",
				"truncated": true,
				"thinking_blocks": []event.ThinkingBlock{
					{Thinking: "incomplete", Signature: ""},
				},
			}},
			{Seq: 3, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "Now acting."}},
			{Seq: 4, Actor: "coordinator", Type: event.ToolCall, Data: map[string]any{
				"name": "read_file", "args": `{}`, "id": "c1",
			}},
			{Seq: 5, Actor: "coordinator", Type: event.ToolResult, Data: map[string]any{
				"id": "c1", "result": "ok",
			}},
		}
	}

	want := []gollama.Message{
		{Role: "user", Content: "go"},
		{Role: "assistant", Content: truncatedStubContent},
		{Role: "user", Content: truncationNudge},
		{
			Role:    "assistant",
			Content: "Now acting.",
			ToolCalls: []gollama.ToolCall{
				{ID: "c1", Type: "function", Function: gollama.ToolCallFunction{Name: "read_file", Arguments: `{}`}},
			},
		},
		{Role: "tool", ToolCallID: "c1", Content: "ok"},
	}

	check := func(t *testing.T, events []event.Event) {
		got := ReplayHistory(events)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ReplayHistory mismatch:\n got=%+v\nwant=%+v", got, want)
		}
		// Assert strict alternation: never two consecutive assistant turns.
		for i := 1; i < len(got); i++ {
			if got[i].Role == "assistant" && got[i-1].Role == "assistant" {
				t.Fatalf("two consecutive assistant messages at %d/%d: %+v", i-1, i, got)
			}
		}
	}

	t.Run("typed", func(t *testing.T) { check(t, mk()) })
	t.Run("from_disk", func(t *testing.T) {
		src := mk()
		decoded := make([]event.Event, len(src))
		for i, ev := range src {
			b, err := json.Marshal(ev)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if err := json.Unmarshal(b, &decoded[i]); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
		}
		check(t, decoded)
	})
}
