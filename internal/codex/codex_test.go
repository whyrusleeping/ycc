package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
)

func testTokens(tok, acct string) TokenSource {
	return func(context.Context) (string, string, error) { return tok, acct, nil }
}

func TestBuildRequest(t *testing.T) {
	opts := gollama.RequestOptions{
		Model:  "gpt-5.3-codex",
		System: "Be terse.",
		Effort: "max",
		Messages: []gollama.Message{
			{Role: "user", Content: "list files"},
			{Role: "assistant", Content: "ok", ToolCalls: []gollama.ToolCall{{
				ID: "call_1", Type: "function",
				Function: gollama.ToolCallFunction{Name: "bash", Arguments: `{"cmd":"ls"}`},
			}}},
			{Role: "tool", ToolCallID: "call_1", Content: "a.txt b.txt"},
		},
		Tools: []gollama.ToolParam{{
			Type: "function",
			Function: &gollama.ToolFunction{
				Name: "bash", Description: "run a command",
				Parameters: map[string]any{"type": "object"},
			},
		}},
	}
	req := buildRequest(opts)
	if !req.Stream || req.Store {
		t.Errorf("stream/store wrong: stream=%v store=%v", req.Stream, req.Store)
	}
	if req.Instructions != "Be terse." {
		t.Errorf("instructions = %q", req.Instructions)
	}
	if req.Reasoning == nil || req.Reasoning.Effort != "xhigh" {
		t.Errorf("reasoning = %+v, want max clamped to xhigh", req.Reasoning)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "bash" || req.Tools[0].Type != "function" {
		t.Errorf("tools = %+v", req.Tools)
	}
	if req.ToolChoice != "auto" {
		t.Errorf("tool_choice = %q", req.ToolChoice)
	}
	// input: user message, assistant message, function_call, function_call_output
	if len(req.Input) != 4 {
		t.Fatalf("input has %d items: %+v", len(req.Input), req.Input)
	}
	if req.Input[0].Type != "message" || req.Input[0].Role != "user" || req.Input[0].Content[0].Type != "input_text" {
		t.Errorf("input[0] = %+v", req.Input[0])
	}
	if req.Input[1].Type != "message" || req.Input[1].Role != "assistant" || req.Input[1].Content[0].Type != "output_text" {
		t.Errorf("input[1] = %+v", req.Input[1])
	}
	if req.Input[2].Type != "function_call" || req.Input[2].CallID != "call_1" || req.Input[2].Name != "bash" {
		t.Errorf("input[2] = %+v", req.Input[2])
	}
	if req.Input[3].Type != "function_call_output" || req.Input[3].CallID != "call_1" || req.Input[3].Output != "a.txt b.txt" {
		t.Errorf("input[3] = %+v", req.Input[3])
	}
	// Empty instructions are defaulted (backend rejects "").
	if got := buildRequest(gollama.RequestOptions{Model: "m"}); strings.TrimSpace(got.Instructions) == "" {
		t.Error("empty instructions not defaulted")
	}
	// Thinking off (no effort) omits the reasoning block.
	if got := buildRequest(gollama.RequestOptions{Model: "m"}); got.Reasoning != nil {
		t.Error("reasoning block present without effort")
	}
}

// sse writes one SSE data frame.
func sse(w http.ResponseWriter, v map[string]any) {
	data, _ := json.Marshal(v)
	fmt.Fprintf(w, "data: %s\n\n", data)
}

func codexStub(t *testing.T, gotReq *map[string]any, gotHdr *http.Header) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %q, want /responses", r.URL.Path)
		}
		*gotHdr = r.Header.Clone()
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		*gotReq = body
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w, map[string]any{"type": "response.created"})
		sse(w, map[string]any{"type": "response.reasoning_summary_text.delta", "delta": "planning"})
		sse(w, map[string]any{"type": "response.output_text.delta", "delta": "hel"})
		sse(w, map[string]any{"type": "response.output_text.delta", "delta": "lo"})
		sse(w, map[string]any{"type": "response.output_item.done", "item": map[string]any{
			"type": "function_call", "call_id": "call_9", "name": "bash", "arguments": `{"cmd":"ls"}`,
		}})
		sse(w, map[string]any{"type": "response.completed", "response": map[string]any{
			"status": "completed",
			"usage": map[string]any{
				"input_tokens":         100,
				"input_tokens_details": map[string]any{"cached_tokens": 40},
				"output_tokens":        7,
				"total_tokens":         107,
			},
		}})
	}))
}

func TestTurnStream(t *testing.T) {
	var gotReq map[string]any
	var gotHdr http.Header
	srv := codexStub(t, &gotReq, &gotHdr)
	defer srv.Close()

	c := New(srv.URL, testTokens("tok-1", "acct-1"))
	var deltas []string
	resp, err := c.TurnStream(gollama.RequestOptions{
		Model:    "gpt-5.3-codex",
		System:   "sys",
		Messages: []gollama.Message{{Role: "user", Content: "hi"}},
	}, func(s string) { deltas = append(deltas, s) })
	if err != nil {
		t.Fatal(err)
	}

	// Headers the backend requires.
	if got := gotHdr.Get("Authorization"); got != "Bearer tok-1" {
		t.Errorf("Authorization = %q", got)
	}
	if got := gotHdr.Get("chatgpt-account-id"); got != "acct-1" {
		t.Errorf("chatgpt-account-id = %q", got)
	}
	if got := gotHdr.Get("OpenAI-Beta"); got != "responses=experimental" {
		t.Errorf("OpenAI-Beta = %q", got)
	}
	if got := gotHdr.Get("originator"); got != "ycc" {
		t.Errorf("originator = %q", got)
	}
	if gotReq["store"] != false || gotReq["stream"] != true {
		t.Errorf("store/stream = %v/%v", gotReq["store"], gotReq["stream"])
	}

	// Response folding.
	if strings.Join(deltas, "") != "hello" {
		t.Errorf("deltas = %v", deltas)
	}
	msg := resp.Choices[0].Message
	if msg.Content != "hello" || msg.Thinking != "planning" {
		t.Errorf("content=%q thinking=%q", msg.Content, msg.Thinking)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "call_9" || msg.ToolCalls[0].Function.Name != "bash" {
		t.Errorf("tool calls = %+v", msg.ToolCalls)
	}
	if resp.StopReason != "tool_calls" {
		t.Errorf("stop reason = %q", resp.StopReason)
	}
	if resp.Usage.PromptTokens != 100 || resp.Usage.CompletionTokens != 7 ||
		resp.Usage.PromptTokensDetails == nil || resp.Usage.PromptTokensDetails.CachedTokens != 40 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if resp.Truncated() {
		t.Error("unexpected truncation")
	}
}

func TestTurnHTTPErrorMatchesClassifier(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"detail":"rate limited"}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := New(srv.URL, testTokens("t", "a"))
	_, err := c.Turn(gollama.RequestOptions{Model: "m", Messages: []gollama.Message{{Role: "user", Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "status code 429") {
		t.Fatalf("want gollama-shaped status error, got %v", err)
	}
}

func TestTurnResponseFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w, map[string]any{"type": "response.failed", "response": map[string]any{
			"error": map[string]any{"code": "server_error", "message": "boom"},
		}})
	}))
	defer srv.Close()
	c := New(srv.URL, testTokens("t", "a"))
	_, err := c.Turn(gollama.RequestOptions{Model: "m", Messages: []gollama.Message{{Role: "user", Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want response.failed error, got %v", err)
	}
}

func TestTurnIncompleteMaxTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w, map[string]any{"type": "response.output_text.delta", "delta": "partial"})
		sse(w, map[string]any{"type": "response.incomplete", "response": map[string]any{
			"status":             "incomplete",
			"incomplete_details": map[string]any{"reason": "max_output_tokens"},
			"usage":              map[string]any{"input_tokens": 1, "output_tokens": 2, "total_tokens": 3},
		}})
	}))
	defer srv.Close()
	c := New(srv.URL, testTokens("t", "a"))
	resp, err := c.Turn(gollama.RequestOptions{Model: "m", Messages: []gollama.Message{{Role: "user", Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Truncated() {
		t.Errorf("want truncated response, got stop reason %q", resp.StopReason)
	}
	if resp.Choices[0].Message.Content != "partial" {
		t.Errorf("content = %q", resp.Choices[0].Message.Content)
	}
}

// The final message item's content is used when no deltas were surfaced.
func TestParseStreamFallsBackToItemText(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"final text"}]}}`,
		``,
		`data: {"type":"response.completed","response":{"status":"completed"}}`,
		``,
	}, "\n")
	resp, err := parseStream(strings.NewReader(body), "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Choices[0].Message.Content != "final text" {
		t.Errorf("content = %q", resp.Choices[0].Message.Content)
	}
}
