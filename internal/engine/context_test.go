package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
)

// errTurner is a fake Turner that always fails with a fixed error, used to
// exercise the loop's error handling.
type errTurner struct{ err error }

func (e *errTurner) TurnCtx(context.Context, gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	return nil, e.err
}

func TestIsContextLengthError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{
			"anthropic prompt too long",
			fmt.Errorf("API returned non-200 status code 400: {\"error\":{\"message\":\"prompt is too long: 250000 tokens > 200000 maximum\"}}"),
			true,
		},
		{
			"openai context_length_exceeded",
			fmt.Errorf("API returned non-200 status code 400: {\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"This model's maximum context length is 128000 tokens.\"}}"),
			true,
		},
		{
			"openai reduce length hint",
			errors.New("API returned non-200 status code 400: please reduce the length of the messages"),
			true,
		},
		{"transient 503", errors.New("API returned non-200 status code 503: service unavailable"), false},
		{"network error", errors.New("error sending request: connection refused"), false},
		{"output truncation", errors.New("turn 3 truncated at the output token cap; raise max_tokens"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsContextLengthError(c.err); got != c.want {
				t.Fatalf("IsContextLengthError(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestRequestContextEstimateIncludesSchemasAndToolArguments(t *testing.T) {
	schema := gollama.ToolParam{Type: "function", Function: &gollama.ToolFunction{
		Name: "Edit", Description: strings.Repeat("schema ", 600),
		Parameters: gollama.ToolFunctionParams{Type: "object", Properties: map[string]any{
			"old_string": map[string]any{"type": "string"}, "new_string": map[string]any{"type": "string"},
		}},
	}}
	base := gollama.RequestOptions{System: "system", Tools: []gollama.ToolParam{schema}, Messages: []gollama.Message{{Role: "user", Content: "edit it"}}}
	first := estimateRequestContext(base, "anthropic")
	second := estimateRequestContext(base, "anthropic")
	if first.Tokens < 1_000 || second.Tokens != first.Tokens {
		t.Fatalf("repeated schema estimates = %d, %d; want same estimate including large schema", first.Tokens, second.Tokens)
	}

	base.Messages = append(base.Messages, gollama.Message{Role: "assistant", ToolCalls: []gollama.ToolCall{
		{ID: "edit", Type: "function", Function: gollama.ToolCallFunction{Name: "Edit", Arguments: strings.Repeat("x", 40_000)}},
		{ID: "write", Type: "function", Function: gollama.ToolCallFunction{Name: "Write", Arguments: strings.Repeat("y", 40_000)}},
	}})
	withBodies := estimateRequestContext(base, "anthropic")
	if withBodies.Tokens-first.Tokens < 19_800 {
		t.Fatalf("large Edit/Write arguments added %d tokens, want about 20000", withBodies.Tokens-first.Tokens)
	}
}

func TestRequestContextEstimateRespectsMessageFieldPrecedence(t *testing.T) {
	large := strings.Repeat("ignored", 10_000)
	legacyImage := strings.Repeat("a", 40_000)
	legacyDoc := gollama.Document{Base64: strings.Repeat("b", 40_000), Title: large}
	multi := []gollama.ContentBlock{{Type: "text", Text: "selected"}}

	tests := []struct {
		name  string
		shape string
		base  gollama.Message
		noisy gollama.Message
	}{
		{
			name: "anthropic user MultiContent replaces legacy fields", shape: "anthropic",
			base: gollama.Message{Role: "user", MultiContent: multi},
			noisy: gollama.Message{Role: "user", Content: large, MultiContent: multi,
				Images: []string{legacyImage}, Documents: []gollama.Document{legacyDoc}},
		},
		{
			name: "anthropic assistant ignores user media fields", shape: "anthropic",
			base: gollama.Message{Role: "assistant", Content: "selected"},
			noisy: gollama.Message{Role: "assistant", Content: "selected",
				MultiContent: []gollama.ContentBlock{{Type: "text", Text: large}, {Type: "image", ImageURL: "https://ignored.invalid/image"}},
				Images:       []string{legacyImage}, Documents: []gollama.Document{legacyDoc}},
		},
		{
			name: "codex user MultiContent replaces Content and ignores legacy media", shape: codexRequestShape,
			base: gollama.Message{Role: "user", MultiContent: multi},
			noisy: gollama.Message{Role: "user", Content: large, MultiContent: multi,
				Images: []string{legacyImage}, Documents: []gollama.Document{legacyDoc}},
		},
		{
			name: "codex assistant ignores MultiContent and legacy media", shape: codexRequestShape,
			base: gollama.Message{Role: "assistant", Content: "selected"},
			noisy: gollama.Message{Role: "assistant", Content: "selected",
				MultiContent: []gollama.ContentBlock{{Type: "text", Text: large}, {Type: "image", ImageURL: "https://ignored.invalid/image"}},
				Images:       []string{legacyImage}, Documents: []gollama.Document{legacyDoc}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := estimateRequestContext(gollama.RequestOptions{Model: "m", Messages: []gollama.Message{tt.base}}, tt.shape)
			noisy := estimateRequestContext(gollama.RequestOptions{Model: "m", Messages: []gollama.Message{tt.noisy}}, tt.shape)
			if noisy.Tokens != base.Tokens || noisy.MediaUncertain != base.MediaUncertain {
				t.Fatalf("ignored fields changed estimate: base=%+v noisy=%+v", base, noisy)
			}
		})
	}
}

type shapedTurner struct{ shape string }

func (s shapedTurner) ContextRequestShape() string { return s.shape }
func (shapedTurner) TurnCtx(context.Context, gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	panic("not used")
}

func TestRequestContextEstimateFiltersProviderStateOnSwitch(t *testing.T) {
	state := gollama.ThinkingBlock{Redacted: codexItemsBlockMarker + `{"model":"m1","items":[{"type":"reasoning","id":"r1","encrypted_content":"` + strings.Repeat("z", 40_000) + `"},{"type":"message","id":"msg1"}]}`}
	loop := &Loop{Client: shapedTurner{shape: codexRequestShape}, Model: "m1", Backend: "openai", Tools: nil}
	loop.SetHistory([]gollama.Message{{Role: "assistant", Content: "done", ThinkingBlocks: []gollama.ThinkingBlock{state}}})
	matching := loop.ContextTokensEstimate()
	// A provider measurement from m1 must not calibrate m2's tokenizer/request.
	loop.lastInput = inputMeasurement{Model: "m1", Shape: codexRequestShape, RawEstimate: matching, Measured: 500_000}
	loop.SetBackendWithContextWindow(shapedTurner{shape: codexRequestShape}, "m2", "other", "openai", 123_000, Thinking{})
	foreignModel := loop.ContextTokensEstimate()
	if matching-foreignModel < 9_900 {
		t.Fatalf("same-model Codex state delta = %d, want encrypted state counted only before model switch", matching-foreignModel)
	}

	loop.SetBackendWithContextWindow(shapedTurner{shape: "anthropic"}, "claude", "claude", "anthropic", 200_000, Thinking{})
	anthropic := loop.ContextTokensEstimate()
	if anthropic != foreignModel {
		t.Fatalf("foreign Codex state leaked after backend switch: anthropic=%d codex-foreign=%d", anthropic, foreignModel)
	}

	native := gollama.RequestOptions{Messages: []gollama.Message{{Role: "assistant", ThinkingBlocks: []gollama.ThinkingBlock{{
		Thinking: strings.Repeat("reasoning", 5_000), Signature: "provider-signature",
	}}}}}
	anthropicNative := estimateRequestContext(native, "anthropic")
	openAI := estimateRequestContext(native, "openai")
	if anthropicNative.Tokens-openAI.Tokens < 9_900 {
		t.Fatalf("Anthropic reasoning replay state delta = %d, want native state counted only by Anthropic", anthropicNative.Tokens-openAI.Tokens)
	}
}

func TestRequestContextEstimateMatchesCodexReplaySelection(t *testing.T) {
	largeReasoning := strings.Repeat("encrypted", 5_000)
	trimmed := gollama.ThinkingBlock{Redacted: codexItemsBlockMarker + `{"model":"m","items":[` +
		`{"type":"reasoning","id":"r","encrypted_content":"` + largeReasoning + `"},` +
		`{"type":"unsupported","id":"x"},{"type":"function_call","id":"f"}]}`}
	base := gollama.RequestOptions{Model: "m", Messages: []gollama.Message{{Role: "assistant"}}}
	withTrimmed := base
	withTrimmed.Messages = []gollama.Message{{Role: "assistant", ThinkingBlocks: []gollama.ThinkingBlock{trimmed}}}
	baseEst := estimateRequestContext(base, codexRequestShape)
	trimmedEst := estimateRequestContext(withTrimmed, codexRequestShape)
	if trimmedEst.Tokens != baseEst.Tokens {
		t.Fatalf("unfollowed reasoning or unsupported items were counted: base=%+v trimmed=%+v", baseEst, trimmedEst)
	}

	// buildAssistantItems appends canonical Content after recorded items when the
	// state has no message item; that fallback makes the reasoning replay valid.
	reasoningOnly := gollama.ThinkingBlock{Redacted: codexItemsBlockMarker + `{"model":"m","items":[` +
		`{"type":"reasoning","id":"r","encrypted_content":"` + largeReasoning + `"}]}`}
	fallbackBase := gollama.RequestOptions{Model: "m", Messages: []gollama.Message{{Role: "assistant", Content: "canonical fallback"}}}
	withFallback := gollama.RequestOptions{Model: "m", Messages: []gollama.Message{{Role: "assistant", Content: "canonical fallback", ThinkingBlocks: []gollama.ThinkingBlock{reasoningOnly}}}}
	fallbackEst := estimateRequestContext(fallbackBase, codexRequestShape)
	replayEst := estimateRequestContext(withFallback, codexRequestShape)
	if replayEst.Tokens-fallbackEst.Tokens < 9_900 {
		t.Fatalf("reasoning before canonical fallback was trimmed: base=%+v replay=%+v", fallbackEst, replayEst)
	}

	first := gollama.ThinkingBlock{Redacted: codexItemsBlockMarker + `{"model":"m","items":[` +
		`{"type":"reasoning","id":"r1","encrypted_content":"kept"},{"type":"message","id":"msg1"}]}`}
	second := gollama.ThinkingBlock{Redacted: codexItemsBlockMarker + `{"model":"m","items":[` +
		`{"type":"reasoning","id":"r2","encrypted_content":"` + largeReasoning + `"},{"type":"message","id":"msg2"}]}`}
	one := gollama.RequestOptions{Model: "m", Messages: []gollama.Message{{Role: "assistant", Content: "answer", ThinkingBlocks: []gollama.ThinkingBlock{first}}}}
	two := one
	two.Messages = []gollama.Message{{Role: "assistant", Content: "answer", ThinkingBlocks: []gollama.ThinkingBlock{first, second}}}
	if oneEst, twoEst := estimateRequestContext(one, codexRequestShape), estimateRequestContext(two, codexRequestShape); oneEst.Tokens != twoEst.Tokens {
		t.Fatalf("second same-model state block was counted: first=%+v both=%+v", oneEst, twoEst)
	}
}

func TestSetHistoryInvalidatesMeasuredContextBaseline(t *testing.T) {
	loop := &Loop{Client: shapedTurner{shape: "openai"}, Model: "m", Backend: "openai"}
	loop.lastInput = inputMeasurement{Model: "m", Shape: "openai", RawEstimate: 100_000, Measured: 1}
	loop.SetHistory([]gollama.Message{{Role: "user", Content: strings.Repeat("x", 400)}})
	if got := loop.ContextTokensEstimate(); got < 100 {
		t.Fatalf("replacement history retained unrelated subtractive baseline: estimate=%d", got)
	}
}

func TestRequestContextEstimateIncludesUncertainMedia(t *testing.T) {
	// A valid 1x1 PNG. Even tiny media must be represented, but the estimate is
	// marked uncertain because provider/model image accounting is not universal.
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	opts := gollama.RequestOptions{Messages: []gollama.Message{{Role: "user", MultiContent: []gollama.ContentBlock{
		{Type: "text", Text: "look"}, {Type: "image", ImageBase64: png, ImageMediaType: "image/png"},
	}}}}
	textOnly := estimateRequestContext(gollama.RequestOptions{Messages: []gollama.Message{{Role: "user", Content: "look"}}}, "openai")
	withImage := estimateRequestContext(opts, "openai")
	if !withImage.MediaUncertain || withImage.MediaTokens == 0 || withImage.Tokens <= textOnly.Tokens {
		t.Fatalf("image estimate = %+v, text-only=%+v", withImage, textOnly)
	}

	opts.Messages[0].MultiContent = append(opts.Messages[0].MultiContent,
		gollama.ContentBlock{Type: "document", DocumentBase64: strings.Repeat("YQ==", 20)})
	withDoc := estimateRequestContext(opts, "anthropic")
	if !withDoc.MediaUncertain || withDoc.MediaTokens <= 0 {
		t.Fatalf("document estimate = %+v", withDoc)
	}
}

func TestLoopContextTelemetrySeparatesMeasuredEstimatedAndCapacity(t *testing.T) {
	response := assistantText("done")
	response.Usage = gollama.Usage{
		PromptTokens: 50_000, CompletionTokens: 20, TotalTokens: 50_020,
		CacheReadInputTokens: 2_000, CacheCreationInputTokens: 3_000,
	}
	rec := &captureRecorder{}
	loop := newLoop(t, &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{response}})
	loop.Backend, loop.ModelName, loop.ContextWindow = "anthropic", "claude", 200_000
	loop.Emitter = event.NewEmitter(rec, "agent")
	loop.Seed("hello")
	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := loop.ContextTokensEstimate(); got < 55_000 {
		t.Fatalf("next estimate %d did not anchor to measured prior input", got)
	}
	var data map[string]any
	for _, ev := range rec.evs {
		if ev.Type == event.ModelTurn {
			data = ev.Data
		}
	}
	if data == nil {
		t.Fatal("missing model_turn")
	}
	if got := data["input_tokens_measured"]; got != 55_000 {
		t.Fatalf("input_tokens_measured = %#v, want 55000", got)
	}
	if got := data["context_window"]; got != 200_000 {
		t.Fatalf("context_window = %#v, want 200000", got)
	}
	if data["context_estimate_approx"] != true {
		t.Fatalf("context estimate not labeled approximate: %#v", data)
	}
	usage := data["usage"].(event.Usage)
	if usage.Input != 50_000 || usage.CacheRead != 2_000 || usage.CacheWrite != 3_000 || usage.Total != 50_020 {
		t.Fatalf("billing usage changed or conflated with measured input: %+v", usage)
	}
}

func TestLoopContextTelemetryHandlesMissingLegacyUsage(t *testing.T) {
	rec := &captureRecorder{}
	loop := newLoop(t, &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{assistantText("done")}})
	loop.Backend = "openai"
	loop.Emitter = event.NewEmitter(rec, "agent")
	loop.Seed("hello")
	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, ev := range rec.evs {
		if ev.Type != event.ModelTurn {
			continue
		}
		if _, ok := ev.Data["input_tokens_measured"]; ok {
			t.Fatalf("legacy zero usage presented as a measurement: %#v", ev.Data)
		}
		if got := ev.Data["context_window"]; got != 0 {
			t.Fatalf("unknown context capacity = %#v, want honest zero", got)
		}
		if got, ok := ev.Data["context_tokens_est"].(int); !ok || got <= 0 {
			t.Fatalf("missing approximate request estimate: %#v", ev.Data["context_tokens_est"])
		}
		return
	}
	t.Fatal("missing model_turn")
}

// A context-length error from the backend fails the loop with a clear, actionable
// message (mentioning "context window exceeded") rather than the opaque provider
// error, and emits a matching SessionError event (task 0010).
func TestLoopFailsOnContextLengthError(t *testing.T) {
	turner := &errTurner{err: errors.New("API returned non-200 status code 400: prompt is too long: 250000 tokens > 200000 maximum")}
	rec := &captureRecorder{}
	loop := newLoop(t, turner)
	loop.Emitter = event.NewEmitter(rec, "agent")
	loop.Seed("do the thing")

	_, err := loop.Run(context.Background())
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "context window exceeded") {
		t.Fatalf("error = %q, want it to mention 'context window exceeded'", err.Error())
	}

	var sawSessionErr bool
	for _, ev := range rec.evs {
		if ev.Type == event.SessionError {
			sawSessionErr = true
			msg, _ := ev.Data["msg"].(string)
			if msg != err.Error() {
				t.Fatalf("SessionError msg = %q, want it to match returned error %q", msg, err.Error())
			}
		}
	}
	if !sawSessionErr {
		t.Fatal("no SessionError event emitted")
	}
}
