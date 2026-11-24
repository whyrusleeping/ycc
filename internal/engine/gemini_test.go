package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/tools"
)

// Gemini 400s on replayed function calls without their thought signature, so a
// reopened session must restore it from the coordinator's tool_call event.
func TestGeminiThoughtSignatureSurvivesReplay(t *testing.T) {
	call := assistantToolCall("sig_test", `{}`)
	call.Choices[0].Message.ToolCalls[0].ThoughtSignature = "sig-gem-1"
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{call, assistantText("done")}}
	reg := tools.New()
	reg.Add(&gollama.Tool{
		Name:   "sig_test",
		Params: tools.Obj(map[string]any{}),
		Call: func(context.Context, any) (*gollama.ToolResult, error) {
			return &gollama.ToolResult{Content: "ok"}, nil
		},
	})
	rec := &captureRecorder{}
	loop := &Loop{Client: turner, Model: "gemini-4-neon", Backend: "gemini", Tools: reg, Emitter: event.NewEmitter(rec, "coordinator")}
	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Round-trip through JSON as the on-disk log does.
	raw, err := json.Marshal(rec.evs)
	if err != nil {
		t.Fatal(err)
	}
	var persisted []event.Event
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, msg := range ReplayHistory(persisted) {
		for _, c := range msg.ToolCalls {
			if c.Function.Name == "sig_test" {
				found = true
				if c.ThoughtSignature != "sig-gem-1" {
					t.Fatalf("replayed signature = %q, want sig-gem-1", c.ThoughtSignature)
				}
			}
		}
	}
	if !found {
		t.Fatal("replayed history has no sig_test call")
	}
}

// Gemini reports cached tokens inside PromptTokens; the recorded Input must be
// the fresh remainder so cost does not bill cached tokens twice.
func TestGeminiUsageSeparatesCachedInput(t *testing.T) {
	resp := assistantText("answered")
	resp.Usage = gollama.Usage{
		PromptTokens: 1000, CompletionTokens: 300, TotalTokens: 1300,
		PromptTokensDetails: &gollama.PromptTokensDetails{CachedTokens: 800},
	}
	rec := &captureRecorder{}
	loop := newLoop(t, &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{resp}})
	loop.Backend = "gemini"
	loop.Emitter = event.NewEmitter(rec, "coordinator")
	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, ev := range rec.evs {
		if ev.Type != event.ModelTurn {
			continue
		}
		want := event.Usage{Input: 200, Output: 300, CacheRead: 800, Total: 1300}
		if u, _ := ev.Data["usage"].(event.Usage); u != want {
			t.Fatalf("usage = %+v, want %+v", u, want)
		}
		return
	}
	t.Fatal("no model_turn event emitted")
}

// Gemini accepts media in function responses, so tool media stays on the tool
// message instead of moving to a follow-up user message.
func TestGeminiToolResultKeepsMedia(t *testing.T) {
	loop := &Loop{Backend: "gemini"}
	loop.appendToolResult("c1", &gollama.ToolResult{
		Content:   "read",
		Images:    []string{"aW1n"},
		Documents: []gollama.Document{{Base64: "cGRm"}},
	})
	if len(loop.history) != 1 || len(loop.history[0].Images) != 1 || len(loop.history[0].Documents) != 1 {
		t.Fatalf("history = %+v, want one tool message carrying the media", loop.history)
	}
}

// Gemini error bodies carry a numeric code; the symbolic status must still
// reach the diagnostic instead of the whole body.
func TestGeminiErrorBodyClassifies(t *testing.T) {
	body := `{"error":{"code":429,"message":"Quota exceeded","status":"RESOURCE_EXHAUSTED"}}`
	got := ClassifyAPIError(&gollama.APIError{StatusCode: 429, Body: body})
	if got.Kind != KindRateLimit || got.Code != "RESOURCE_EXHAUSTED" || got.Message != "Quota exceeded" {
		t.Fatalf("got %+v", got)
	}
}
