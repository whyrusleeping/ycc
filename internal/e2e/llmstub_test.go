// Package e2e is the end-to-end TUI test harness (task 0175). It drives the
// REAL ycc binary under a pseudo-terminal against a scripted OpenAI-compatible
// LLM stub, pipes the binary's terminal output into an in-process VT emulator,
// and synchronizes tests on screen-content predicates over the emulator's text
// grid. Optional PNG screenshots of the real rendered screen are written (via
// the internal/tui/snapshot rasterizer) when YCC_TUI_SNAPSHOT_DIR is set.
//
// The LLM is the ONLY mocked seam: everything else — the built binary, the
// one-shot in-process daemon, the engine loop, tool execution, and the Bubble
// Tea runtime — is real. See docs/e2e-tui.md for the layered design and how to
// add scenarios.
package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
)

// scriptedToolCall is one tool call the stubbed model requests in a turn. The
// name must match a tool the selected mode mounts (e.g. "Read" in chat mode),
// and Arguments is the raw JSON argument string the engine will pass through.
type scriptedToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// scriptedTurn is one pre-programmed assistant response the stub returns for the
// next /chat/completions request. Text is the assistant message body (may be
// empty for a tool-only turn); ToolCalls are the tool invocations the engine
// should execute before requesting the following turn. Finish overrides the
// finish_reason ("stop"/"tool_calls"); when empty it is derived from ToolCalls.
type scriptedTurn struct {
	Text      string
	ToolCalls []scriptedToolCall
	Finish    string
}

// llmStub is an OpenAI-compatible /chat/completions server returning a
// pre-programmed sequence of responses. It honors BOTH request modes: a
// "stream":true request is answered as chat.completion.chunk SSE (the shape the
// engine's streaming turn consumes); otherwise a plain chat.completion JSON
// body. Once the script is exhausted it returns a canned "done" text turn rather
// than erroring, so a stray probe never crashes the run.
type llmStub struct {
	*httptest.Server

	mu       sync.Mutex
	script   []scriptedTurn
	idx      int
	requests []map[string]any // decoded request bodies, in arrival order
}

// newLLMStub starts a stub server programmed with the given turns. The caller
// closes it (via t.Cleanup in the harness).
func newLLMStub(script []scriptedTurn) *llmStub {
	s := &llmStub{script: script}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// next returns the next scripted turn, advancing the cursor; once exhausted it
// returns a terminal "done" turn so the agent loop can settle.
func (s *llmStub) next() scriptedTurn {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idx < len(s.script) {
		t := s.script[s.idx]
		s.idx++
		return t
	}
	return scriptedTurn{Text: "Done.", Finish: "stop"}
}

// requestCount returns how many /chat/completions requests have been received.
func (s *llmStub) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *llmStub) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	s.mu.Lock()
	s.requests = append(s.requests, decoded)
	s.mu.Unlock()

	turn := s.next()
	stream, _ := decoded["stream"].(bool)
	if stream {
		writeSSE(w, turn)
		return
	}
	writeJSON(w, turn)
}

// finishReason resolves the finish_reason for a turn (tool_calls when it carries
// tool calls, else stop) unless one was set explicitly.
func (t scriptedTurn) finishReason() string {
	if t.Finish != "" {
		return t.Finish
	}
	if len(t.ToolCalls) > 0 {
		return "tool_calls"
	}
	return "stop"
}

// --- wire shapes (subset of the OpenAI chat.completions schema) ---

type oaiFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type oaiToolCall struct {
	Index    int         `json:"index"`
	ID       string      `json:"id,omitempty"`
	Type     string      `json:"type,omitempty"`
	Function oaiFunction `json:"function"`
}

type oaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// writeSSE emits the turn as chat.completion.chunk server-sent events: an
// assistant content chunk (when there is text), one chunk per tool call, a
// finish chunk, then a usage-only chunk and the terminal [DONE] sentinel.
func writeSSE(w http.ResponseWriter, t scriptedTurn) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	send := func(chunk map[string]any) {
		b, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if flusher != nil {
			flusher.Flush()
		}
	}
	chunk := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{
			"id":     "chatcmpl-e2e",
			"object": "chat.completion.chunk",
			"model":  "stub-model",
			"choices": []map[string]any{{
				"index":         0,
				"delta":         delta,
				"finish_reason": finish,
			}},
		}
	}

	if t.Text != "" {
		send(chunk(map[string]any{"role": "assistant", "content": t.Text}, nil))
	}
	for i, tc := range t.ToolCalls {
		send(chunk(map[string]any{"tool_calls": []oaiToolCall{{
			Index: i, ID: tc.ID, Type: "function",
			Function: oaiFunction{Name: tc.Name, Arguments: tc.Arguments},
		}}}, nil))
	}
	send(chunk(map[string]any{}, t.finishReason()))
	// Final usage-only chunk with an empty choices array.
	send(map[string]any{
		"id": "chatcmpl-e2e", "object": "chat.completion.chunk", "model": "stub-model",
		"choices": []any{},
		"usage":   oaiUsage{PromptTokens: 12, CompletionTokens: 8, TotalTokens: 20},
	})
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// writeJSON emits the turn as a non-streaming chat.completion body.
func writeJSON(w http.ResponseWriter, t scriptedTurn) {
	msg := map[string]any{"role": "assistant", "content": t.Text}
	if len(t.ToolCalls) > 0 {
		calls := make([]oaiToolCall, len(t.ToolCalls))
		for i, tc := range t.ToolCalls {
			calls[i] = oaiToolCall{
				Index: i, ID: tc.ID, Type: "function",
				Function: oaiFunction{Name: tc.Name, Arguments: tc.Arguments},
			}
		}
		msg["tool_calls"] = calls
	}
	resp := map[string]any{
		"id": "chatcmpl-e2e", "object": "chat.completion", "model": "stub-model",
		"choices": []map[string]any{{
			"index":         0,
			"message":       msg,
			"finish_reason": t.finishReason(),
		}},
		"usage": oaiUsage{PromptTokens: 12, CompletionTokens: 8, TotalTokens: 20},
	}
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(resp)
	_, _ = w.Write(b)
}
