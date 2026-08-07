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
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
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
			{Role: "user", MultiContent: []gollama.ContentBlock{
				{Type: "text", Text: "list files"},
				{Type: "image", ImageBase64: "aW1hZ2U=", ImageMediaType: "image/png"},
			}},
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
	if len(req.Include) != 1 || req.Include[0] != "reasoning.encrypted_content" {
		t.Errorf("include = %v", req.Include)
	}
	if req.Instructions != "Be terse." {
		t.Errorf("instructions = %q", req.Instructions)
	}
	if req.Reasoning == nil || req.Reasoning.Effort != "xhigh" || req.Reasoning.Summary != "detailed" {
		t.Errorf("reasoning = %+v, want xhigh effort + detailed summary", req.Reasoning)
	}
	if req.StreamOptions == nil || req.StreamOptions.ReasoningSummaryDelivery != "sequential_cutoff" {
		t.Errorf("stream_options = %+v, want sequential reasoning summaries", req.StreamOptions)
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
	if req.Input[0].Type != "message" || req.Input[0].Role != "user" || len(req.Input[0].Content) != 2 || req.Input[0].Content[0].Type != "input_text" || req.Input[0].Content[1].Type != "input_image" || req.Input[0].Content[1].ImageURL != "data:image/png;base64,aW1hZ2U=" {
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
	// Thinking off (no effort) omits both reasoning controls.
	if got := buildRequest(gollama.RequestOptions{Model: "m"}); got.Reasoning != nil || got.StreamOptions != nil {
		t.Errorf("reasoning controls present without effort: reasoning=%+v stream_options=%+v", got.Reasoning, got.StreamOptions)
	}
}

func TestBuildRequestMaxOutputTokens(t *testing.T) {
	tests := []struct {
		name    string
		options *gollama.Options
		want    string
	}{
		{name: "configured", options: &gollama.Options{MaxTokens: 12_345}, want: `"max_output_tokens":12345`},
		{name: "nil options"},
		{name: "zero cap", options: &gollama.Options{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(buildRequest(gollama.RequestOptions{Model: "m", Options: tt.options}))
			if err != nil {
				t.Fatal(err)
			}
			if tt.want == "" {
				if strings.Contains(string(body), `"max_output_tokens"`) {
					t.Fatalf("max_output_tokens must be omitted: %s", body)
				}
				return
			}
			if !strings.Contains(string(body), tt.want) {
				t.Fatalf("serialized request = %s, want %s", body, tt.want)
			}
		})
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
		sse(w, map[string]any{"type": "response.reasoning_summary_text.delta", "item_id": "rs_9", "summary_index": 0, "delta": "planning"})
		sse(w, map[string]any{"type": "response.output_item.done", "item": map[string]any{
			"id": "rs_9", "type": "reasoning", "encrypted_content": "encrypted-secret", "summary": []map[string]any{{"type": "summary_text", "text": "planning"}},
		}})
		sse(w, map[string]any{"type": "response.output_text.delta", "delta": "hel"})
		sse(w, map[string]any{"type": "response.output_text.delta", "delta": "lo"})
		sse(w, map[string]any{"type": "response.output_item.done", "item": map[string]any{
			"id": "fc_9", "type": "function_call", "call_id": "call_9", "name": "bash", "arguments": `{"cmd":"ls"}`,
		}})
		sse(w, map[string]any{"type": "response.completed", "response": map[string]any{
			"status": "completed",
			"usage": map[string]any{
				"input_tokens":          100,
				"input_tokens_details":  map[string]any{"cached_tokens": 40},
				"output_tokens":         7,
				"output_tokens_details": map[string]any{"reasoning_tokens": 5},
				"total_tokens":          107,
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

	// Response folding. Stream callbacks are full accumulated snapshots, not
	// raw fragments, because turn_delta consumers replace their live tail.
	if got, want := fmt.Sprint(deltas), "[hel hello]"; got != want {
		t.Errorf("deltas = %v, want %v", deltas, want)
	}
	msg := resp.Choices[0].Message
	if msg.Content != "hello" || msg.Thinking != "planning" {
		t.Errorf("content=%q thinking=%q", msg.Content, msg.Thinking)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "call_9" || msg.ToolCalls[0].Function.Name != "bash" {
		t.Errorf("tool calls = %+v", msg.ToolCalls)
	}
	if len(msg.ThinkingBlocks) != 1 {
		t.Fatalf("thinking blocks = %+v", msg.ThinkingBlocks)
	}
	blockModel, responseItems, ok := DecodeItemsBlock(msg.ThinkingBlocks[0])
	if !ok || blockModel != "gpt-5.3-codex" || len(responseItems) != 2 || responseItems[0].ID != "rs_9" || responseItems[0].EncryptedContent != "encrypted-secret" || responseItems[1].ID != "fc_9" {
		t.Fatalf("items block model=%q items=%+v ok=%v", blockModel, responseItems, ok)
	}
	if strings.Contains(msg.Thinking, "encrypted-secret") || strings.Contains(msg.Content, "encrypted-secret") {
		t.Fatal("encrypted provider state leaked into visible transcript")
	}
	if resp.StopReason != "tool_calls" {
		t.Errorf("stop reason = %q", resp.StopReason)
	}
	if resp.Usage.PromptTokens != 100 || resp.Usage.CompletionTokens != 7 ||
		resp.Usage.PromptTokensDetails == nil || resp.Usage.PromptTokensDetails.CachedTokens != 40 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if got := c.ReasoningTokens(); got != 5 {
		t.Errorf("reasoning tokens = %d, want 5", got)
	}
	if resp.Truncated() {
		t.Error("unexpected truncation")
	}
}

func TestParseStreamPreservesCompletedReasoningSummarySections(t *testing.T) {
	var stream strings.Builder
	write := func(v map[string]any) {
		data, _ := json.Marshal(v)
		fmt.Fprintf(&stream, "data: %s\n\n", data)
	}
	// Deltas may be partial; done text must replace them. Multiple summary parts
	// must remain distinct rather than being glued into one unreadable heading.
	write(map[string]any{"type": "response.reasoning_summary_part.added", "item_id": "r1", "summary_index": 0})
	write(map[string]any{"type": "response.reasoning_summary_text.delta", "item_id": "r1", "summary_index": 0, "delta": "**Inspecting**"})
	write(map[string]any{"type": "response.reasoning_summary_text.done", "item_id": "r1", "summary_index": 0, "text": "**Inspecting code**\n\nI traced the request path."})
	write(map[string]any{"type": "response.reasoning_summary_text.delta", "item_id": "r1", "summary_index": 1, "delta": "**Planning fix**"})
	write(map[string]any{"type": "response.reasoning_summary_text.done", "item_id": "r1", "summary_index": 1, "text": "**Planning fix**\n\nI will preserve every section."})
	write(map[string]any{"type": "response.output_text.delta", "delta": "done"})
	write(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed"}})

	resp, _, err := parseStream(strings.NewReader(stream.String()), "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := resp.Choices[0].Message.Thinking
	want := "**Inspecting code**\n\nI traced the request path.\n\n**Planning fix**\n\nI will preserve every section."
	if got != want {
		t.Fatalf("thinking:\n%q\nwant:\n%q", got, want)
	}
	if strings.Count(got, "**Inspecting") != 1 {
		t.Fatalf("partial delta was duplicated instead of replaced: %q", got)
	}
}

func TestParseStreamUsesReasoningItemSummaryFallback(t *testing.T) {
	var stream strings.Builder
	write := func(v map[string]any) {
		data, _ := json.Marshal(v)
		fmt.Fprintf(&stream, "data: %s\n\n", data)
	}
	write(map[string]any{"type": "response.output_item.done", "item": map[string]any{
		"id": "r1", "type": "reasoning", "summary": []map[string]any{
			{"type": "summary_text", "text": "first section"},
			{"type": "summary_text", "text": "second section"},
		},
	}})
	write(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed"}})

	resp, _, err := parseStream(strings.NewReader(stream.String()), "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resp.Choices[0].Message.Thinking, "first section\n\nsecond section"; got != want {
		t.Fatalf("thinking = %q, want %q", got, want)
	}
}

func TestBuildRequestReplaysTwoToolTurnsAndReopen(t *testing.T) {
	model := "gpt-5.5"
	call := func(id, name, args string) gollama.ToolCall {
		return gollama.ToolCall{ID: id, Type: "function", Function: gollama.ToolCallFunction{Name: name, Arguments: args}}
	}
	firstBlock := EncodeItemsBlock(model, []ResponseItem{
		{Type: "reasoning", ID: "rs_1", EncryptedContent: "enc-1", Summary: []SummaryPart{}},
		{Type: "function_call", ID: "fc_1", CallID: "provider-call-1"},
	})
	secondBlock := EncodeItemsBlock(model, []ResponseItem{
		{Type: "reasoning", ID: "rs_2", EncryptedContent: "enc-2", Summary: []SummaryPart{{Type: "summary_text", Text: "next step"}}},
		{Type: "function_call", ID: "fc_2", CallID: "provider-call-2"},
	})
	live := []gollama.Message{
		{Role: "user", Content: "start"},
		{Role: "assistant", ThinkingBlocks: []gollama.ThinkingBlock{firstBlock}, ToolCalls: []gollama.ToolCall{call("call_1", "read", `{"path":"a"}`)}},
		{Role: "tool", ToolCallID: "call_1", Content: "one"},
		{Role: "assistant", ThinkingBlocks: []gollama.ThinkingBlock{secondBlock}, ToolCalls: []gollama.ToolCall{call("call_2", "write", `{"path":"b"}`)}},
		{Role: "tool", ToolCallID: "call_2", Content: "two"},
	}
	liveReq := buildRequest(gollama.RequestOptions{Model: model, Messages: live})
	wantTypes := []string{"message", "reasoning", "function_call", "function_call_output", "reasoning", "function_call", "function_call_output"}
	if len(liveReq.Input) != len(wantTypes) {
		t.Fatalf("input = %+v", liveReq.Input)
	}
	for i, want := range wantTypes {
		if liveReq.Input[i].Type != want {
			t.Fatalf("input[%d].type=%q want %q; input=%+v", i, liveReq.Input[i].Type, want, liveReq.Input)
		}
	}
	if liveReq.Input[1].ID != "rs_1" || liveReq.Input[1].EncryptedContent != "enc-1" || liveReq.Input[1].Summary == nil || len(*liveReq.Input[1].Summary) != 0 {
		t.Fatalf("first reasoning = %+v", liveReq.Input[1])
	}
	if liveReq.Input[2].ID != "fc_1" || liveReq.Input[2].CallID != "call_1" || liveReq.Input[4].ID != "rs_2" || liveReq.Input[5].ID != "fc_2" || liveReq.Input[5].CallID != "call_2" {
		t.Fatalf("provider/canonical ids not reconciled: %+v", liveReq.Input)
	}
	body, err := json.Marshal(liveReq)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"summary":[]`) || !strings.Contains(string(body), `"include":["reasoning.encrypted_content"]`) {
		t.Fatalf("required stateless fields absent: %s", body)
	}

	// Exercise the same generic-map path used after reading events.jsonl. The
	// model_turn stores only provider state; tool_call events restore canonical
	// calls exactly as the live loop does.
	events := []event.Event{
		{Seq: 1, Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "start"}},
		{Seq: 2, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "", "tool_calls": 1, "thinking_blocks": []event.ThinkingBlock{{Redacted: firstBlock.Redacted}}}},
		{Seq: 3, Actor: "coordinator", Type: event.ToolCall, Data: map[string]any{"id": "call_1", "name": "read", "args": `{"path":"a"}`}},
		{Seq: 4, Actor: "coordinator", Type: event.ToolResult, Data: map[string]any{"id": "call_1", "result": "one"}},
		{Seq: 5, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "", "tool_calls": 1, "thinking_blocks": []event.ThinkingBlock{{Redacted: secondBlock.Redacted}}}},
		{Seq: 6, Actor: "coordinator", Type: event.ToolCall, Data: map[string]any{"id": "call_2", "name": "write", "args": `{"path":"b"}`}},
		{Seq: 7, Actor: "coordinator", Type: event.ToolResult, Data: map[string]any{"id": "call_2", "result": "two"}},
	}
	decoded := make([]event.Event, len(events))
	for i, ev := range events {
		data, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &decoded[i]); err != nil {
			t.Fatal(err)
		}
	}
	reopenedReq := buildRequest(gollama.RequestOptions{Model: model, Messages: engine.ReplayHistory(decoded)})
	liveInput, _ := json.Marshal(liveReq.Input)
	reopenedInput, _ := json.Marshal(reopenedReq.Input)
	if string(reopenedInput) != string(liveInput) {
		t.Fatalf("reopen input differs from live:\nlive: %s\nopen: %s", liveInput, reopenedInput)
	}
}

func TestBuildInputSyntheticPreloadExchange(t *testing.T) {
	call := gollama.ToolCall{ID: "preload_1", Type: "function", Function: gollama.ToolCallFunction{Name: "Read", Arguments: `{"file_path":"x.go"}`}}
	items := buildInput([]gollama.Message{
		{Role: "user", Content: "preload nudge"},
		{Role: "assistant", ToolCalls: []gollama.ToolCall{call}},
		{Role: "tool", ToolCallID: "preload_1", Content: "file contents"},
		{Role: "user", Content: "implement seed"},
	}, "model")
	if len(items) != 4 {
		t.Fatalf("item count = %d, want 4: %+v", len(items), items)
	}
	if items[0].Type != "message" || items[0].Role != "user" || items[1].Type != "function_call" || items[1].CallID != "preload_1" || items[1].Name != "Read" || items[2].Type != "function_call_output" || items[2].CallID != "preload_1" || items[2].Output != "file contents" || items[3].Type != "message" || items[3].Role != "user" {
		t.Fatalf("synthetic exchange converted incorrectly: %+v", items)
	}
}

func TestBuildInputItemsBlockSafety(t *testing.T) {
	call := gollama.ToolCall{ID: "call", Type: "function", Function: gollama.ToolCallFunction{Name: "bash", Arguments: `{}`}}
	foreign := gollama.ThinkingBlock{Thinking: "anthropic thinking", Signature: "sig"}
	wrongModel := EncodeItemsBlock("old-model", []ResponseItem{{Type: "reasoning", ID: "old", EncryptedContent: "stale"}, {Type: "function_call", ID: "old-call"}})
	items := buildInput([]gollama.Message{{Role: "assistant", ThinkingBlocks: []gollama.ThinkingBlock{foreign, wrongModel}, ToolCalls: []gollama.ToolCall{call}}}, "new-model")
	if len(items) != 1 || items[0].Type != "function_call" || items[0].ID != "" || items[0].CallID != "call" {
		t.Fatalf("foreign/wrong-model state was not ignored: %+v", items)
	}

	dangling := EncodeItemsBlock("m", []ResponseItem{{Type: "reasoning", ID: "rs", EncryptedContent: "enc", Summary: []SummaryPart{}}})
	items = buildInput([]gollama.Message{{Role: "assistant", ThinkingBlocks: []gollama.ThinkingBlock{dangling}}}, "m")
	if len(items) != 0 {
		t.Fatalf("dangling reasoning was replayed: %+v", items)
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

// A stream-level `error` frame arrives over an HTTP 200 stream, so there is no
// status code to parse. The adapter must keep the provider's error CODE in the
// message so the engine classifies a backend `server_error` as transient and
// RETRIES it, rather than stranding the session until the user types "continue"
// (task 0225). This asserts the cross-package contract end to end.
func TestTurnStreamErrorIsClassifiedRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w, map[string]any{"type": "error", "error": map[string]any{
			"type":    "server_error",
			"code":    "server_error",
			"message": "An error occurred while processing your request. You can retry your request, or contact us through our help center at help.openai.com if the error persists. Please include the request ID abc123 in your message.",
		}})
	}))
	defer srv.Close()
	c := New(srv.URL, testTokens("t", "a"))
	_, err := c.Turn(gollama.RequestOptions{Model: "m", Messages: []gollama.Message{{Role: "user", Content: "x"}}})
	if err == nil {
		t.Fatal("want a stream error")
	}
	if !strings.Contains(err.Error(), "server_error") || !strings.Contains(err.Error(), "request ID abc123") {
		t.Fatalf("error must carry the provider code and message, got %v", err)
	}
	info := engine.ClassifyAPIError(err)
	if info.Kind != engine.KindServer || !info.Retryable {
		t.Fatalf("ClassifyAPIError = %+v, want kind=%s retryable=true", info, engine.KindServer)
	}
}

// An unrecognised `error` frame still surfaces the raw payload rather than being
// swallowed into an empty message.
func TestTurnStreamErrorFallsBackToRawFrame(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w, map[string]any{"type": "error", "detail": "weird shape"})
	}))
	defer srv.Close()
	c := New(srv.URL, testTokens("t", "a"))
	_, err := c.Turn(gollama.RequestOptions{Model: "m", Messages: []gollama.Message{{Role: "user", Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "weird shape") {
		t.Fatalf("want the raw frame preserved, got %v", err)
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

func TestTurnIncompleteMaxTokensReasoningOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w, map[string]any{
			"type": "response.reasoning_summary_text.delta", "item_id": "rs_1", "summary_index": 0, "delta": "planning",
		})
		sse(w, map[string]any{"type": "response.incomplete", "response": map[string]any{
			"status":             "incomplete",
			"incomplete_details": map[string]any{"reason": "max_output_tokens"},
			"usage": map[string]any{
				"input_tokens":          11,
				"input_tokens_details":  map[string]any{"cached_tokens": 7},
				"output_tokens":         13,
				"output_tokens_details": map[string]any{"reasoning_tokens": 13},
				"total_tokens":          24,
			},
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
	if got := resp.Choices[0].Message.Content; got != "" {
		t.Errorf("visible content = %q, want empty", got)
	}
	if got := resp.Choices[0].Message.Thinking; got != "planning" {
		t.Errorf("thinking = %q, want planning", got)
	}
	if resp.Usage.PromptTokens != 11 || resp.Usage.CompletionTokens != 13 || resp.Usage.TotalTokens != 24 ||
		resp.Usage.PromptTokensDetails == nil || resp.Usage.PromptTokensDetails.CachedTokens != 7 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if got := c.ReasoningTokens(); got != 13 {
		t.Errorf("reasoning tokens = %d, want 13", got)
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
	resp, _, err := parseStream(strings.NewReader(body), "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Choices[0].Message.Content != "final text" {
		t.Errorf("content = %q", resp.Choices[0].Message.Content)
	}
}
