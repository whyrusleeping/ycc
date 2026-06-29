package engine

// This file implements "resume = replay" (spec §4.5/§5/§18.6): reconstructing a
// coordinator loop's conversation history from a session's persisted event log so
// a finished/idle session can be re-opened and continued on the SAME log.
//
// Known lossy edges, deferred to follow-up: tool-result images/PDFs are NOT
// restored (only their counts are recorded on tool_result events), and the
// internal truncation-retry nudge (the synthetic "your response was cut off"
// stub + follow-up user message) is not logged, so it is not replayed. These are
// acceptable limitations for now — the reconstructed history is still valid for
// continuing the conversation on the next turn.

import (
	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
)

// ReplayHistory reconstructs the coordinator agent loop's conversation history
// from a session's events, in order: user inputs, the coordinator's assistant
// turns (with thinking blocks + tool calls), and tool results. Subagent activity
// (actor implementer/reviewer:*) and non-conversational events are ignored — the
// reconstructed history is the COORDINATOR's view, mirroring what Loop builds
// live. Dangling tool calls (a tool_call with no recorded tool_result, e.g. a
// session reopened mid-turn) get a synthetic tool result appended so the
// conversation stays valid for the next turn (Anthropic/OpenAI require every
// tool_use to be answered).
func ReplayHistory(events []event.Event) []gollama.Message {
	var history []gollama.Message
	// assistantIdx is the index in history of the current coordinator assistant
	// message that subsequent coordinator tool_calls attach to (-1 if none yet).
	assistantIdx := -1
	// answered records tool_call ids that have a matching tool_result, so we can
	// repair any dangling calls at the end.
	answered := map[string]bool{}

	for _, ev := range events {
		switch ev.Type {
		case event.UserInput:
			// All user inputs belong to the coordinator conversation regardless of
			// actor (they are emitted as actor "user").
			history = append(history, gollama.Message{Role: "user", Content: str(ev.Data, "text")})
			assistantIdx = -1
		case event.ModelTurn:
			if ev.Actor != "coordinator" {
				continue // subagent turn — not part of the coordinator history
			}
			// A turn cut off at the output token cap (truncated) may carry an
			// unsigned/incomplete thinking block; drop the blocks here to match the
			// live loop's sanitized stub (which omits the cut-off block so Anthropic
			// doesn't reject it on the next request).
			var blocks []gollama.ThinkingBlock
			if !boolv(ev.Data, "truncated") {
				blocks = parseThinkingBlocks(ev.Data["thinking_blocks"])
			}
			history = append(history, gollama.Message{
				Role:           "assistant",
				Content:        str(ev.Data, "text"),
				ThinkingBlocks: blocks,
			})
			assistantIdx = len(history) - 1
		case event.ToolCall:
			if ev.Actor != "coordinator" {
				continue
			}
			if assistantIdx < 0 {
				continue // orphan tool call with no preceding assistant message
			}
			call := gollama.ToolCall{
				ID:   str(ev.Data, "id"),
				Type: "function",
				Function: gollama.ToolCallFunction{
					Name:      str(ev.Data, "name"),
					Arguments: str(ev.Data, "args"),
				},
			}
			history[assistantIdx].ToolCalls = append(history[assistantIdx].ToolCalls, call)
		case event.ToolResult:
			if ev.Actor != "coordinator" {
				continue
			}
			id := str(ev.Data, "id")
			answered[id] = true
			history = append(history, gollama.Message{Role: "tool", ToolCallID: id, Content: str(ev.Data, "result")})
		}
	}

	// Repair dangling tool calls on the trailing assistant message: any tool_call
	// id that never saw a matching tool_result gets a synthetic result so the
	// reconstructed conversation is valid for the next turn.
	if assistantIdx >= 0 {
		for _, call := range history[assistantIdx].ToolCalls {
			if !answered[call.ID] {
				history = append(history, gollama.Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Content:    "(no result recorded; session was reopened)",
				})
				answered[call.ID] = true
			}
		}
	}

	return history
}

// parseThinkingBlocks defensively decodes the thinking_blocks field from a
// model_turn event into gollama.ThinkingBlock values. It handles both the
// freshly-emitted typed shape ([]event.ThinkingBlock) and — importantly — the
// JSON-decoded-from-disk shape ([]any of map[string]any with keys
// "thinking"/"signature"/"data").
func parseThinkingBlocks(v any) []gollama.ThinkingBlock {
	switch blocks := v.(type) {
	case []event.ThinkingBlock:
		if len(blocks) == 0 {
			return nil
		}
		out := make([]gollama.ThinkingBlock, len(blocks))
		for i, b := range blocks {
			out[i] = gollama.ThinkingBlock{Thinking: b.Thinking, Signature: b.Signature, Redacted: b.Redacted}
		}
		return out
	case []gollama.ThinkingBlock:
		return blocks
	case []any:
		var out []gollama.ThinkingBlock
		for _, e := range blocks {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			out = append(out, gollama.ThinkingBlock{
				Thinking:  strv(m, "thinking"),
				Signature: strv(m, "signature"),
				Redacted:  strv(m, "data"),
			})
		}
		return out
	default:
		return nil
	}
}

// str reads a string field from an event data map (mirrors event.str, kept local
// to the engine package).
func str(m map[string]any, k string) string { return strv(m, k) }

func strv(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[k].(string); ok {
		return s
	}
	return ""
}

// boolv reads a bool field from an event data map, defensively handling the
// JSON-decoded shape (a plain bool).
func boolv(m map[string]any, k string) bool {
	if m == nil {
		return false
	}
	b, _ := m[k].(bool)
	return b
}
