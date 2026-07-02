package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/tools"
)

// scriptedTurner returns a pre-programmed sequence of responses, one per Turn
// call, recording the requests it saw so the test can assert on context growth.
type scriptedTurner struct {
	responses []*gollama.ResponseMessageGenerate
	calls     int
	lastMsgs  []gollama.Message
	lastOpts  gollama.RequestOptions
}

func (s *scriptedTurner) Turn(opts gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	s.lastMsgs = opts.Messages
	s.lastOpts = opts
	r := s.responses[s.calls]
	s.calls++
	return r, nil
}

func assistantToolCall(name, args string) *gollama.ResponseMessageGenerate {
	return &gollama.ResponseMessageGenerate{Choices: []gollama.GenChoice{{Message: gollama.Message{
		Role:      "assistant",
		ToolCalls: []gollama.ToolCall{{ID: "c1", Type: "function", Function: gollama.ToolCallFunction{Name: name, Arguments: args}}},
	}}}}
}

func assistantText(text string) *gollama.ResponseMessageGenerate {
	return &gollama.ResponseMessageGenerate{Choices: []gollama.GenChoice{{Message: gollama.Message{Role: "assistant", Content: text}}}}
}

// truncatedTurn models an Anthropic turn cut off at the output token cap: the
// model emitted no tool call and stop_reason is "max_tokens".
func truncatedTurn(text string) *gollama.ResponseMessageGenerate {
	r := assistantText(text)
	r.StopReason = "max_tokens"
	return r
}

func newLoop(t *testing.T, turner Turner) *Loop {
	t.Helper()
	reg := tools.New()
	reg.Add(tools.Worker(&tools.Workspace{Root: t.TempDir()})...)
	return &Loop{
		Client:  turner,
		Model:   "test",
		Tools:   reg,
		Emitter: event.NewEmitter(nil, "agent"),
	}
}

// A control tool (finish) ends the loop and surfaces its report.
func TestLoopStopsOnFinish(t *testing.T) {
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{
		assistantToolCall("finish", `{"report":"all done"}`),
	}}
	res, err := newLoop(t, turner).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Report != "all done" {
		t.Fatalf("report = %q, want %q", res.Report, "all done")
	}
	if res.Turns != 1 {
		t.Fatalf("turns = %d, want 1", res.Turns)
	}
}

// report_blocked is a control tool that ends the loop AND marks the run blocked,
// carrying the reason in Report so callers can escalate distinctly from a finish.
func TestLoopStopsOnReportBlocked(t *testing.T) {
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{
		assistantToolCall("report_blocked", `{"reason":"need a decision on the schema"}`),
	}}
	res, err := newLoop(t, turner).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Blocked {
		t.Fatalf("Blocked = false, want true")
	}
	if res.Report != "need a decision on the schema" {
		t.Fatalf("report = %q", res.Report)
	}
}

// A turn with no tool calls yields, returning the assistant text as the report.
func TestLoopYieldsOnNoToolCalls(t *testing.T) {
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{
		assistantText("nothing left to do"),
	}}
	res, err := newLoop(t, turner).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Report != "nothing left to do" {
		t.Fatalf("report = %q", res.Report)
	}
}

// A non-truncated turn with empty content and no tool calls (e.g. stop_reason
// "refusal", or the whole budget consumed by a thinking block with no follow-up
// text) must never surface as a blank assistant message: the loop synthesizes a
// non-empty, stop-reason-aware report and stores it as the assistant turn.
func TestLoopEmptyYieldSynthesizesReport(t *testing.T) {
	r := assistantText("") // no content, no tool calls
	r.StopReason = "refusal"
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{r}}
	loop := newLoop(t, turner)
	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(res.Report) == "" {
		t.Fatalf("expected a non-empty report, got %q", res.Report)
	}
	if !strings.Contains(strings.ToLower(res.Report), "declined") {
		t.Fatalf("report should mention the refusal/declination, got %q", res.Report)
	}
	// The final assistant message in history must be non-empty (no blank message).
	hist := loop.History()
	last := hist[len(hist)-1]
	if last.Role != "assistant" || strings.TrimSpace(last.Content) == "" {
		t.Fatalf("expected a non-empty trailing assistant message, got %+v", last)
	}
}

// An unfamiliar/odd stop reason on an empty turn surfaces the raw reason in the
// synthesized report so it isn't hidden behind a blank message.
func TestLoopEmptyYieldUnknownStopReason(t *testing.T) {
	r := assistantText("")
	r.StopReason = "content_filter"
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{r}}
	res, err := newLoop(t, turner).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Report, "content_filter") {
		t.Fatalf("report should surface the raw stop reason, got %q", res.Report)
	}
}

// A turn truncated at the token cap with no tool call is NOT a clean yield: the
// loop nudges the model to continue, and once it acts the run proceeds normally.
func TestLoopContinuesAfterTruncation(t *testing.T) {
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{
		truncatedTurn(""), // ran out of budget mid-thinking, emitted nothing actionable
		assistantToolCall("finish", `{"report":"recovered"}`),
	}}
	loop := newLoop(t, turner)
	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Report != "recovered" {
		t.Fatalf("report = %q, want recovered", res.Report)
	}
	// Invariants of the retry context (turner.lastMsgs is what the recovery turn saw):
	var sawNudge bool
	var prevRole string
	for _, m := range turner.lastMsgs {
		// No partial/unsigned thinking block may be echoed back.
		if len(m.ThinkingBlocks) != 0 || strings.TrimSpace(m.Thinking) != "" {
			t.Fatalf("truncated thinking was kept in history: %+v", m)
		}
		// Roles must alternate — two consecutive user turns would be rejected by
		// the Anthropic API.
		if m.Role == "user" && prevRole == "user" {
			t.Fatalf("consecutive user messages in history: %+v", turner.lastMsgs)
		}
		prevRole = m.Role
		if m.Role == "user" && strings.Contains(m.Content, "cut off") {
			sawNudge = true
		}
	}
	if !sawNudge {
		t.Fatalf("expected a continue-nudge user message in history: %+v", turner.lastMsgs)
	}
}

// Persistent truncation (the model never recovers) eventually fails loudly with
// a truncation error rather than silently returning an empty report.
func TestLoopFailsOnRepeatedTruncation(t *testing.T) {
	resps := make([]*gollama.ResponseMessageGenerate, 0, maxTruncRetries+1)
	for i := 0; i < maxTruncRetries+1; i++ {
		resps = append(resps, truncatedTurn(""))
	}
	turner := &scriptedTurner{responses: resps}
	res, err := newLoop(t, turner).Run(context.Background())
	if err == nil {
		t.Fatalf("expected a truncation error, got nil (res=%+v)", res)
	}
	if res == nil || !res.Truncated {
		t.Fatalf("expected res.Truncated=true, got %+v", res)
	}
}

// Tool results are fed back into context, and the loop continues across turns
// until a control tool stops it.
func TestLoopFeedsResultsAndContinues(t *testing.T) {
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{
		assistantToolCall("Write", `{"file_path":"a.txt","content":"hi"}`),
		assistantToolCall("finish", `{"report":"wrote a.txt"}`),
	}}
	loop := newLoop(t, turner)
	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Turns != 2 {
		t.Fatalf("turns = %d, want 2", res.Turns)
	}
	// By the 2nd turn the history must contain: user seed is absent (none seeded),
	// assistant tool_call, and the tool result message.
	var sawToolResult bool
	for _, m := range turner.lastMsgs {
		if m.Role == "tool" && m.ToolCallID == "c1" {
			sawToolResult = true
		}
	}
	if !sawToolResult {
		t.Fatal("tool result was not fed back into context")
	}
}

// model_turn and tool_result events carry an elapsed duration_ms so per-session
// performance is visible in the logs (task 0055).
func TestLoopRecordsTiming(t *testing.T) {
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{
		assistantToolCall("Write", `{"file_path":"a.txt","content":"hi"}`),
		assistantToolCall("finish", `{"report":"done"}`),
	}}
	rec := &captureRecorder{}
	loop := newLoop(t, turner)
	loop.Emitter = event.NewEmitter(rec, "agent")
	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var sawTurn, sawTool bool
	for _, ev := range rec.evs {
		switch ev.Type {
		case event.ModelTurn:
			sawTurn = true
			if _, ok := ev.Data["duration_ms"].(int64); !ok {
				t.Fatalf("model_turn duration_ms = %v (%T), want int64", ev.Data["duration_ms"], ev.Data["duration_ms"])
			}
			if ev.Data["duration_ms"].(int64) < 0 {
				t.Fatalf("model_turn duration_ms = %d, want >= 0", ev.Data["duration_ms"].(int64))
			}
		case event.ToolResult:
			sawTool = true
			if _, ok := ev.Data["duration_ms"].(int64); !ok {
				t.Fatalf("tool_result duration_ms = %v (%T), want int64", ev.Data["duration_ms"], ev.Data["duration_ms"])
			}
			if ev.Data["duration_ms"].(int64) < 0 {
				t.Fatalf("tool_result duration_ms = %d, want >= 0", ev.Data["duration_ms"].(int64))
			}
		}
	}
	if !sawTurn {
		t.Fatal("no model_turn event emitted")
	}
	if !sawTool {
		t.Fatal("no tool_result event emitted")
	}
}

// captureRecorder records emitted events in memory for assertions.
type captureRecorder struct{ evs []event.Event }

func (c *captureRecorder) Record(actor string, t event.Type, data map[string]any) event.Event {
	ev := event.Event{Seq: len(c.evs) + 1, Actor: actor, Type: t, Data: data}
	c.evs = append(c.evs, ev)
	return ev
}

// The engine carries the loop's per-model reasoning settings into every request.
func TestLoopSetsThinkingOptions(t *testing.T) {
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{assistantText("hi")}}
	loop := newLoop(t, turner)
	loop.Thinking = "adaptive"
	loop.Effort = "high"
	loop.ThinkingDisplay = "summarized"
	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if turner.lastOpts.Thinking != "adaptive" || turner.lastOpts.Effort != "high" || turner.lastOpts.ThinkingDisplay != "summarized" {
		t.Fatalf("opts thinking=%q effort=%q display=%q", turner.lastOpts.Thinking, turner.lastOpts.Effort, turner.lastOpts.ThinkingDisplay)
	}
}

// SetBackend updates the reasoning settings used by the next turn.
func TestLoopSetBackendUpdatesThinking(t *testing.T) {
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{assistantText("hi")}}
	loop := newLoop(t, turner) // starts with no thinking
	loop.SetBackend(turner, "test2", "claude", "anthropic", Thinking{Thinking: "adaptive", Effort: "max", ThinkingDisplay: "summarized"})
	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if turner.lastOpts.Model != "test2" || turner.lastOpts.Effort != "max" {
		t.Fatalf("opts model=%q effort=%q", turner.lastOpts.Model, turner.lastOpts.Effort)
	}
	if loop.ModelName != "claude" || loop.Backend != "anthropic" {
		t.Fatalf("SetBackend identity model_name=%q backend=%q", loop.ModelName, loop.Backend)
	}
}

// A turn that returns a reasoning summary emits a dedicated thinking event
// before the model_turn event.
func TestLoopEmitsThinkingEvent(t *testing.T) {
	resp := assistantText("the answer")
	resp.Choices[0].Message.Thinking = "let me reason about this"
	resp.Choices[0].Message.ThinkingBlocks = []gollama.ThinkingBlock{{Thinking: "let me reason about this", Signature: "sig"}}
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{resp}}
	rec := &captureRecorder{}
	loop := newLoop(t, turner)
	loop.Emitter = event.NewEmitter(rec, "agent")
	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var thinkIdx, turnIdx = -1, -1
	for i, ev := range rec.evs {
		switch ev.Type {
		case event.Thinking:
			thinkIdx = i
			if got, _ := ev.Data["text"].(string); got != "let me reason about this" {
				t.Fatalf("thinking text = %q", got)
			}
		case event.ModelTurn:
			turnIdx = i
		}
	}
	if thinkIdx < 0 {
		t.Fatal("no thinking event emitted")
	}
	if turnIdx >= 0 && thinkIdx > turnIdx {
		t.Fatal("thinking event should precede model_turn")
	}
}

// No thinking event is emitted when the turn has no reasoning summary.
func TestLoopNoThinkingEventWhenEmpty(t *testing.T) {
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{assistantText("plain")}}
	rec := &captureRecorder{}
	loop := newLoop(t, turner)
	loop.Emitter = event.NewEmitter(rec, "agent")
	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, ev := range rec.evs {
		if ev.Type == event.Thinking {
			t.Fatal("unexpected thinking event for empty reasoning")
		}
	}
}

// An empty-text, non-redacted thinking block must be stripped from the history
// (and the recorded thinking_blocks) before the loop echoes the turn back:
// Anthropic 400s on "content.N.thinking.thinking: field required" if it isn't.
// Redacted blocks and real text blocks in the same turn are preserved so a
// legitimately signed reasoning turn still round-trips.
func TestLoopStripsEmptyThinkingBlocks(t *testing.T) {
	// Turn 1: a tool call carrying a mix of blocks — one empty (illegal to
	// replay), one real, one redacted. Turn 2: yield.
	call := assistantToolCall("ls", `{}`)
	call.Choices[0].Message.ThinkingBlocks = []gollama.ThinkingBlock{
		{Thinking: "   ", Signature: "sig-empty"}, // whitespace-only ⇒ dropped
		{Thinking: "real reasoning", Signature: "sig-ok"},
		{Redacted: "opaque-data"},
	}
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{
		call,
		assistantText("done"),
	}}
	loop := newLoop(t, turner)
	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The assistant turn stored in history is history[1] (history[0] is the tool
	// result-bearing exchange is not relevant here; find the assistant turn).
	var asst *gollama.Message
	for i := range loop.history {
		if loop.history[i].Role == "assistant" && len(loop.history[i].ToolCalls) > 0 {
			asst = &loop.history[i]
			break
		}
	}
	if asst == nil {
		t.Fatalf("no assistant tool-call turn in history: %+v", loop.history)
	}
	if len(asst.ThinkingBlocks) != 2 {
		t.Fatalf("kept %d thinking blocks, want 2 (empty one dropped): %+v", len(asst.ThinkingBlocks), asst.ThinkingBlocks)
	}
	for _, b := range asst.ThinkingBlocks {
		if b.Redacted == "" && strings.TrimSpace(b.Thinking) == "" {
			t.Fatalf("an empty thinking block survived: %+v", b)
		}
	}
}

// model_turn events carry per-turn token usage and model identity sourced from
// resp.Usage and the loop's model labelling (spec §20.1).
func TestLoopModelTurnCarriesUsage(t *testing.T) {
	resp := assistantText("answered")
	resp.Usage = gollama.Usage{
		PromptTokens:             100,
		CompletionTokens:         20,
		TotalTokens:              120,
		CacheReadInputTokens:     30,
		CacheCreationInputTokens: 10,
	}
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{resp}}
	rec := &captureRecorder{}
	loop := newLoop(t, turner)
	loop.ModelName = "claude"
	loop.Backend = "anthropic"
	loop.Emitter = event.NewEmitter(rec, "agent")
	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var found bool
	for _, ev := range rec.evs {
		if ev.Type != event.ModelTurn {
			continue
		}
		found = true
		if ev.Data["model_name"] != "claude" || ev.Data["backend"] != "anthropic" || ev.Data["model_id"] != "test" {
			t.Fatalf("identity = name=%v backend=%v id=%v", ev.Data["model_name"], ev.Data["backend"], ev.Data["model_id"])
		}
		u, ok := ev.Data["usage"].(event.Usage)
		if !ok {
			t.Fatalf("usage type = %T, want event.Usage", ev.Data["usage"])
		}
		want := event.Usage{Input: 100, Output: 20, CacheRead: 30, CacheWrite: 10, Total: 120}
		if u != want {
			t.Fatalf("usage = %+v, want %+v", u, want)
		}
	}
	if !found {
		t.Fatal("no model_turn event emitted")
	}
}

// Backends that don't report usage record zeros without error.
func TestLoopModelTurnZeroUsage(t *testing.T) {
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{assistantText("plain")}}
	rec := &captureRecorder{}
	loop := newLoop(t, turner)
	loop.Emitter = event.NewEmitter(rec, "agent")
	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, ev := range rec.evs {
		if ev.Type != event.ModelTurn {
			continue
		}
		u, ok := ev.Data["usage"].(event.Usage)
		if !ok {
			t.Fatalf("usage type = %T, want event.Usage", ev.Data["usage"])
		}
		if (u != event.Usage{}) {
			t.Fatalf("usage = %+v, want all zeros", u)
		}
	}
}

// The loop terminates with an error when it exceeds MaxTurns (model never stops).
func TestLoopMaxTurns(t *testing.T) {
	loopForever := make([]*gollama.ResponseMessageGenerate, 10)
	for i := range loopForever {
		loopForever[i] = assistantToolCall("Bash", `{"command":"echo hi"}`)
	}
	turner := &scriptedTurner{responses: loopForever}
	loop := newLoop(t, turner)
	loop.MaxTurns = 3
	_, err := loop.Run(context.Background())
	if err == nil {
		t.Fatal("expected max-turns error, got nil")
	}
}

// The default backstop is high (well above the old 40) but still finite, so a
// degenerate infinite tool-call loop is eventually stopped.
func TestLoopDefaultMaxTurnsBackstop(t *testing.T) {
	if defaultMaxTurns < 100 {
		t.Fatalf("defaultMaxTurns = %d, want a high default (>=100)", defaultMaxTurns)
	}
	loopForever := make([]*gollama.ResponseMessageGenerate, defaultMaxTurns+5)
	for i := range loopForever {
		loopForever[i] = assistantToolCall("Bash", `{"command":"echo hi"}`)
	}
	turner := &scriptedTurner{responses: loopForever}
	loop := newLoop(t, turner) // MaxTurns unset => default backstop
	res, err := loop.Run(context.Background())
	if err == nil {
		t.Fatal("expected max-turns error from default backstop, got nil")
	}
	if res.Turns != defaultMaxTurns {
		t.Fatalf("turns = %d, want default backstop %d", res.Turns, defaultMaxTurns)
	}
}

// MaxTurns is a per-Run budget, not cumulative: a second Run on the same loop
// (as send_to_implementer does for a revise round) gets a fresh turn count.
func TestLoopMaxTurnsResetsPerRun(t *testing.T) {
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{
		// Run #1: two turns then finish.
		assistantToolCall("Bash", `{"command":"echo a"}`),
		assistantText("done round one"),
		// Run #2: two turns then finish — would exceed a cumulative cap of 3.
		assistantToolCall("Bash", `{"command":"echo b"}`),
		assistantText("done round two"),
	}}
	loop := newLoop(t, turner)
	loop.MaxTurns = 3
	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	loop.Post("revise please")
	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("run 2: %v (cap should reset per Run)", err)
	}
	if res.Report != "done round two" {
		t.Fatalf("report = %q", res.Report)
	}
}

// mediaTool returns a tool result carrying an image and a PDF, modelling the
// multimodal Read path.
func mediaTool() *gollama.Tool {
	return &gollama.Tool{
		Name:   "Read",
		Params: gollama.ToolFunctionParams{Type: "object", Properties: map[string]any{}, Required: []string{}},
		Call: func(ctx context.Context, _ any) (*gollama.ToolResult, error) {
			return &gollama.ToolResult{
				Content:   "Read image pic.png; it is attached.",
				Images:    []string{"aW1hZ2U="},
				Documents: []gollama.Document{{Base64: "ZG9j", MediaType: "application/pdf"}},
			}, nil
		},
	}
}

func mediaLoop(t *testing.T, turner Turner, backend string) *Loop {
	t.Helper()
	reg := tools.New()
	reg.Add(mediaTool())
	return &Loop{Client: turner, Model: "test", Backend: backend, Tools: reg, Emitter: event.NewEmitter(nil, "agent")}
}

// On Anthropic, tool-result media attaches directly to the tool message.
func TestToolResultMediaAttachesToToolMessageAnthropic(t *testing.T) {
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{
		assistantToolCall("Read", `{}`),
		assistantText("looked at it"),
	}}
	loop := mediaLoop(t, turner, "anthropic")
	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	var tool *gollama.Message
	for i := range turner.lastMsgs {
		if turner.lastMsgs[i].Role == "tool" {
			tool = &turner.lastMsgs[i]
		}
	}
	if tool == nil {
		t.Fatal("no tool message in history")
	}
	if len(tool.Images) != 1 || len(tool.Documents) != 1 {
		t.Fatalf("expected media on tool message, got images=%d docs=%d", len(tool.Images), len(tool.Documents))
	}
}

// On OpenAI-compatible backends, images come back as a follow-up user message
// (tool messages can't carry images there); documents are dropped.
func TestToolResultMediaFollowupUserMessageOpenAI(t *testing.T) {
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{
		assistantToolCall("Read", `{}`),
		assistantText("looked at it"),
	}}
	loop := mediaLoop(t, turner, "openai")
	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	var sawToolText, sawUserImage bool
	for _, m := range turner.lastMsgs {
		if m.Role == "tool" && len(m.Images) != 0 {
			t.Fatal("openai tool message should not carry images")
		}
		if m.Role == "tool" && m.Content != "" {
			sawToolText = true
		}
		if m.Role == "user" && len(m.MultiContent) > 0 {
			for _, b := range m.MultiContent {
				if b.Type == "image" && b.ImageBase64 == "aW1hZ2U=" {
					sawUserImage = true
				}
			}
		}
	}
	if !sawToolText || !sawUserImage {
		t.Fatalf("expected tool text + follow-up user image (toolText=%v userImage=%v)", sawToolText, sawUserImage)
	}
}

// TestPendingResponse covers the reopen-mid-turn detector: the loop "owes a
// turn" whenever its history ends on a user input or an unanswered tool result.
func TestPendingResponse(t *testing.T) {
	cases := []struct {
		name string
		hist []gollama.Message
		want bool
	}{
		{"empty", nil, false},
		{"ends user", []gollama.Message{{Role: "user", Content: "hi"}}, true},
		{"ends tool", []gollama.Message{{Role: "user"}, {Role: "assistant"}, {Role: "tool", Content: "r"}}, true},
		{"ends assistant", []gollama.Message{{Role: "user"}, {Role: "assistant", Content: "done"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := &Loop{}
			l.SetHistory(tc.hist)
			if got := l.PendingResponse(); got != tc.want {
				t.Fatalf("PendingResponse() = %v, want %v", got, tc.want)
			}
		})
	}
}
