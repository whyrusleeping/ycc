// Package engine implements the core agent loop (spec §7.2): run a model turn,
// dispatch any tool calls, feed results back, and repeat until the model yields
// with no tool calls or a control tool signals stop.
package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/tools"
)

// Turner is the single capability the loop needs from a backend: run one
// (non-streaming) model turn. *gollama.Client satisfies it via its Turn method,
// and it keeps the loop testable with a scripted fake.
type Turner interface {
	Turn(gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error)
}

// Steer lets a session pause and steer a running loop at safe checkpoints
// (spec §18.7). Checkpoint is consulted between turns and after each tool
// result. When a pause is pending it blocks until resume (or ctx
// cancellation, returned as a normal stop) and returns any correction
// messages to append before the next turn. A nil Steer is a cheap no-op.
type Steer interface {
	Checkpoint(ctx context.Context) ([]string, error)
}

// Loop drives one agent (coordinator or subagent) over a backend.
type Loop struct {
	Client Turner
	Model  string // resolved backend model id (e.g. "claude-sonnet-4-...")
	// ModelName is the logical model name per spec §13 (e.g. "claude", "gpt"),
	// recorded on model_turn events so per-turn usage is attributable per model
	// independent of the resolved id. Backend is the logical backend family
	// (e.g. "anthropic", "openai"). Both are display/accounting metadata only.
	ModelName string
	Backend   string
	System    string
	Tools     *tools.Registry
	Emitter   *event.Emitter
	MaxTurns  int // 0 => default
	MaxTok    int // per-turn max tokens; 0 => backend default

	// Anthropic extended/adaptive reasoning (spec §7, §13). Thinking == ""
	// disables reasoning; "adaptive" enables it. Effort tunes depth/spend
	// ("low".."max"); ThinkingDisplay ("summarized") opts into reasoning
	// summaries. Honored by the anthropic backend, ignored by others.
	Thinking        string
	Effort          string
	ThinkingDisplay string

	// Steer, when set, is consulted at safe checkpoints (top of each turn and
	// after each tool result) so a session can pause and steer the running loop
	// (spec §18.7). Nil ⇒ a cheap no-op; the hot loop is unaffected.
	Steer Steer

	mu      sync.Mutex // guards Client/Model swaps mid-loop (settings overlay, §18.2)
	history []gollama.Message
}

// steerCheckpoint consults the Steer hook (if any). It blocks while a pause is
// pending, appends any returned correction messages to the conversation before
// the next turn, and returns ctx cancellation as a normal stop. A nil Steer is
// a no-op.
func (l *Loop) steerCheckpoint(ctx context.Context) error {
	if l.Steer == nil {
		return nil
	}
	msgs, err := l.Steer.Checkpoint(ctx)
	if err != nil {
		return err
	}
	for _, m := range msgs {
		l.Post(m)
	}
	return nil
}

// SetBackend swaps the loop's backend client, model id, logical model identity,
// and per-model reasoning settings while preserving the conversation history, so
// a mid-session role-config change takes effect on the next turn (spec §18.2).
// Safe to call concurrently with Run.
func (l *Loop) SetBackend(client Turner, model, modelName, backend string, think Thinking) {
	l.mu.Lock()
	l.Client = client
	l.Model = model
	l.ModelName = modelName
	l.Backend = backend
	l.Thinking = think.Thinking
	l.Effort = think.Effort
	l.ThinkingDisplay = think.ThinkingDisplay
	l.mu.Unlock()
}

// SetThinking swaps only the loop's reasoning settings (thinking/effort/display)
// while preserving the backend client, model id, and conversation history, so a
// mid-session thinking-level change takes effect on the next turn (spec §18.2).
// Safe to call concurrently with Run.
func (l *Loop) SetThinking(think Thinking) {
	l.mu.Lock()
	l.Thinking = think.Thinking
	l.Effort = think.Effort
	l.ThinkingDisplay = think.ThinkingDisplay
	l.mu.Unlock()
}

// Thinking carries per-model reasoning settings for SetBackend so a coordinator
// model swap also updates effort/thinking. It mirrors config.Thinking but lives
// here to avoid an engine→config import cycle.
type Thinking struct {
	Thinking        string
	Effort          string
	ThinkingDisplay string
}

// modelIdentity is the loop's current model labelling for usage attribution.
type modelIdentity struct {
	ID      string // resolved backend model id
	Name    string // logical model name (§13)
	Backend string // logical backend family
}

func (l *Loop) backend() (Turner, string, modelIdentity, Thinking) {
	l.mu.Lock()
	defer l.mu.Unlock()
	id := modelIdentity{ID: l.Model, Name: l.ModelName, Backend: l.Backend}
	return l.Client, l.Model, id, Thinking{Thinking: l.Thinking, Effort: l.Effort, ThinkingDisplay: l.ThinkingDisplay}
}

// defaultMaxTurns is the per-Run backstop applied when Loop.MaxTurns is unset
// (0). It is deliberately high (200) so that normal multi-step work — the
// implementer's read → edit → build → test → fix cycles across several files —
// is not guillotined mid-task. It is NOT removed entirely: it remains as a
// runaway/cost guard so a model stuck in a degenerate infinite tool-call loop
// can't burn tokens forever.
//
// The cap is per Run, not cumulative: each Run starts its turn counter at 1
// (see Run below), so a send_to_implementer revise round that calls Run again on
// the same Loop gets a fresh budget rather than inheriting the previous round's
// turn count.
//
// Interaction with task 0010 (context-window management): raising the turn cap
// means more turns accumulate more conversation history. Until 0010 lands,
// a high turn cap can trade a turn-limit abort for a context-window-limit abort
// on a very long run. The turn cap is the runaway backstop; context budgeting is
// 0010's concern.
const defaultMaxTurns = 200

// maxTruncRetries bounds how many consecutive times the loop will nudge the
// model to continue after a turn was cut off at the output token cap before it
// emitted any tool call (commonly: the whole budget went to an extended-thinking
// block). Past this, the loop gives up and returns a truncation error rather
// than spinning forever.
const maxTruncRetries = 2

// truncatedStubContent and truncationNudge are the two messages the live loop
// appends at a mid-Run output-token truncation boundary: a sanitized assistant
// stub (with non-empty content so backends don't reject it) followed by an
// internal user "nudge" telling the model to continue. The nudge is posted via
// Loop.Post and is NOT recorded in the event log, so replay.go (ReplayHistory)
// reuses these constants to synthesize the nudge when it reconstructs a
// truncation-retry boundary, preserving strict user/assistant alternation.
const (
	truncatedStubContent = "(my previous response was cut off at the output token limit)"
	truncationNudge      = "Your previous response was cut off at the output token limit before you took any action. Keep your reasoning brief and call a tool now to make concrete progress."
)

// noContentYieldReport produces a concise, ALWAYS non-empty description of why a
// turn ended when the model emitted neither a tool call nor any visible content
// (and the turn was NOT truncated at the token cap). It branches on the provider
// stop reason so a refusal reads differently from an ordinary end-of-turn, and an
// unfamiliar reason is surfaced verbatim rather than hidden behind a blank
// message. The result is used both as the assistant turn's content (recorded on
// the model_turn event for lossless replay) and as the loop's Result.Report.
func noContentYieldReport(stopReason string) string {
	switch strings.ToLower(strings.TrimSpace(stopReason)) {
	case "refusal":
		return "(the model declined to respond and produced no content or tool call)"
	case "", "end_turn", "stop", "stop_sequence":
		return "(the model ended its turn without any content or tool call)"
	default:
		return fmt.Sprintf("(the model ended its turn without any content or tool call; stop reason: %s)", stopReason)
	}
}

// Result is the outcome of a completed loop.
type Result struct {
	Report     string // final report (from a control tool) or last assistant text
	Turns      int
	NextMode   string // if set, a control tool requested a transition to this mode
	NextPrompt string // if set, the verbatim seed prompt for the next mode's loop
	Truncated  bool   // the final turn hit the token cap before producing an action
	// NoContent is set when the loop ended on a non-truncated turn that produced
	// neither a tool call nor any visible content (e.g. stop_reason "refusal", or
	// the whole budget consumed by a thinking block with no follow-up text). In
	// that case Report holds a synthesized, non-empty stop-reason description
	// rather than real model output, so callers can distinguish a genuine yield
	// with substance from a degenerate empty yield (e.g. the implementer no-op
	// guard).
	NoContent bool
}

// Seed appends an initial user message (the task prompt) before Run.
func (l *Loop) Seed(prompt string) { l.Post(prompt) }

// toEventThinking maps gollama reasoning blocks to the serializable event shape
// so they round-trip through the JSONL log (and back, for reopen replay).
func toEventThinking(blocks []gollama.ThinkingBlock) []event.ThinkingBlock {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]event.ThinkingBlock, len(blocks))
	for i, b := range blocks {
		out[i] = event.ThinkingBlock{Thinking: b.Thinking, Signature: b.Signature, Redacted: b.Redacted}
	}
	return out
}

// SetHistory replaces the loop's conversation history. Used by session reopen to
// install a history reconstructed from the event log before the first new turn
// (spec §4.5). Safe to call concurrently with Run (guarded like backend()),
// though in practice it is set before Run begins.
func (l *Loop) SetHistory(h []gollama.Message) {
	l.mu.Lock()
	l.history = h
	l.mu.Unlock()
}

// History returns a copy of the loop's current conversation history.
func (l *Loop) History() []gollama.Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]gollama.Message, len(l.history))
	copy(out, l.history)
	return out
}

// Post appends a user message to the conversation. Used both to seed the initial
// task and to inject follow-up input between Run calls (a "prod"), so a session
// can continue the same agent across multiple turns.
func (l *Loop) Post(content string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.history = append(l.history, gollama.Message{Role: "user", Content: content})
}

// PendingResponse reports whether the conversation is currently waiting on an
// assistant turn — i.e. the last message is a user input or a tool result the
// model has not yet responded to.
//
// Used by session resume: a reopened session whose reconstructed history ends
// mid-turn (the model owes a response to a user message or to tool results) must
// let the model respond BEFORE accepting new user input. Posting a fresh user
// message in that state would place two non-assistant turns back to back —
// Anthropic renders tool results as user-role messages, so a tool result (or
// bare user message) immediately followed by a new user message is two
// consecutive user turns, which backends reject with a 400
// invalid_request_error ("messages: roles must alternate").
func (l *Loop) PendingResponse() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.history) == 0 {
		return false
	}
	switch l.history[len(l.history)-1].Role {
	case "user", "tool":
		return true
	}
	return false
}

// appendToolResult appends a tool result message to the conversation, carrying
// any native media (images/PDFs) the tool returned so multimodal Reads round-
// trip to the model (spec §8).
//
// Anthropic accepts image/document blocks inside a tool_result, so we attach them
// directly to the tool message. OpenAI-compatible APIs do not allow media in a
// tool-role message, so for those backends we attach images as a follow-up user
// message (the model still sees them, right after the result). Documents (PDFs)
// are Anthropic-only in gollama and are dropped for other backends.
func (l *Loop) appendToolResult(callID string, res *gollama.ToolResult) {
	msg := gollama.Message{Role: "tool", ToolCallID: callID, Content: res.Content}
	if len(res.Images) == 0 && len(res.Documents) == 0 {
		l.history = append(l.history, msg)
		return
	}
	if strings.EqualFold(l.Backend, "anthropic") {
		msg.Images = res.Images
		msg.Documents = res.Documents
		l.history = append(l.history, msg)
		return
	}
	// Non-Anthropic: keep the tool result text-only, then carry images in a
	// follow-up user message that OpenAI-compatible backends accept.
	l.history = append(l.history, msg)
	if len(res.Images) == 0 {
		return
	}
	blocks := []gollama.ContentBlock{{Type: "text", Text: "(attached file contents from the previous Read)"}}
	for _, img := range res.Images {
		blocks = append(blocks, gollama.ContentBlock{Type: "image", ImageBase64: img})
	}
	l.history = append(l.history, gollama.Message{Role: "user", MultiContent: blocks})
}

// Run executes the loop to completion. It returns when a control tool signals
// stop, the model produces a turn with no tool calls, or MaxTurns is reached.
func (l *Loop) Run(ctx context.Context) (*Result, error) {
	maxTurns := l.MaxTurns
	if maxTurns == 0 {
		maxTurns = defaultMaxTurns
	}

	// truncRetries counts consecutive turns cut off at the token cap with no
	// tool call; it resets whenever a turn completes normally.
	truncRetries := 0

	// turn resets to 1 on every Run, so MaxTurns is a per-Run budget rather
	// than a cumulative one across revise rounds (see defaultMaxTurns).
	for turn := 1; turn <= maxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// Safe checkpoint between turns: pause-to-steer if requested (spec §18.7).
		if err := l.steerCheckpoint(ctx); err != nil {
			return nil, err
		}

		client, modelID, ident, think := l.backend()
		opts := gollama.RequestOptions{
			Model:           modelID,
			System:          l.System,
			Messages:        l.history,
			Tools:           l.Tools.APIDefs(),
			Thinking:        think.Thinking,
			Effort:          think.Effort,
			ThinkingDisplay: think.ThinkingDisplay,
		}
		if l.MaxTok > 0 {
			opts.Options = &gollama.Options{MaxTokens: l.MaxTok}
		}

		start := time.Now()
		resp, err := client.Turn(opts)
		elapsedMS := time.Since(start).Milliseconds()
		if err != nil {
			// A context-window-exceeded failure (history too large for the model)
			// is terminal and opaque from the provider. Surface a clear, actionable
			// message instead of the raw 400 so the user knows to start fresh or
			// narrow scope rather than retry (task 0010). All other errors keep
			// their existing behavior.
			if IsContextLengthError(err) {
				msg := fmt.Sprintf("context window exceeded for model %s: the conversation history (~%d tokens) is too large to continue. This session cannot proceed automatically — start a fresh session or narrow the task scope.", modelID, approxContextTokens(l.System, l.history))
				l.Emitter.Emit(event.SessionError, map[string]any{"msg": msg, "duration_ms": elapsedMS})
				return nil, errors.New(msg)
			}
			l.Emitter.Emit(event.SessionError, map[string]any{"msg": err.Error(), "duration_ms": elapsedMS})
			return nil, fmt.Errorf("turn %d: %w", turn, err)
		}
		if len(resp.Choices) == 0 {
			return nil, errors.New("model returned no choices")
		}
		msg := resp.Choices[0].Message

		// Surface the model's reasoning summary (if any) as its own event so the
		// TUI can show it distinctly, collapsed by default (spec §18). The
		// ThinkingBlocks themselves round-trip via the appended assistant
		// message; this event is purely for display.
		if strings.TrimSpace(msg.Thinking) != "" {
			l.Emitter.Emit(event.Thinking, map[string]any{
				"text":   msg.Thinking,
				"blocks": len(msg.ThinkingBlocks),
			})
		}

		// Capture per-turn token usage (spec §20.1). resp.Usage is zero-valued for
		// backends that don't report it, so usage records zeros without error.
		u := resp.Usage
		truncated := resp.Truncated()
		// A non-truncated turn that produced no tool call AND no content is a
		// degenerate stop: most often the whole output budget went to an
		// extended-thinking block that the model didn't follow with any visible
		// text, or the provider returned an odd stop reason (refusal/other). We
		// MUST NOT surface this as a bare empty assistant message — that confuses
		// the model (and any caller reading Result.Report). Synthesize a concise,
		// non-empty description of WHY the turn ended BEFORE the model_turn event
		// is emitted, so the synthesized text is recorded on the event and reopen/
		// replay reconstructs the identical non-empty turn (no replay.go change).
		// Truncated turns are intentionally left alone here: they have their own
		// sanitized-stub + nudge handling below that replay depends on.
		noContentYield := false
		if len(msg.ToolCalls) == 0 && !truncated && strings.TrimSpace(msg.Content) == "" {
			msg.Content = noContentYieldReport(resp.StopReason)
			noContentYield = true
		}
		// contextEst is a coarse estimate of the prompt size (system + history)
		// that produced this turn, surfaced so long-session growth toward the
		// context window is visible in telemetry (task 0010).
		contextEst := approxContextTokens(l.System, l.history)
		l.Emitter.Emit(event.ModelTurn, map[string]any{
			"text":               msg.Content,
			"tool_calls":         len(msg.ToolCalls),
			"model_name":         ident.Name,
			"backend":            ident.Backend,
			"model_id":           ident.ID,
			"stop_reason":        resp.StopReason,
			"truncated":          truncated,
			"duration_ms":        elapsedMS,
			"context_tokens_est": contextEst,
			// thinking_blocks carries the signed/redacted reasoning blocks on the
			// ALWAYS-emitted model_turn (not the optional Thinking display event,
			// which is skipped when display is "omitted" yet still produces signed
			// blocks needed to replay the turn on Anthropic). This lets reopen
			// reconstruct the conversation losslessly (spec §5.1).
			"thinking_blocks": toEventThinking(msg.ThinkingBlocks),
			"usage": event.Usage{
				Input:      u.PromptTokens,
				Output:     u.CompletionTokens,
				CacheRead:  u.GetCachedTokens(),
				CacheWrite: u.CacheCreationInputTokens,
				Total:      u.TotalTokens,
			},
		})

		if len(msg.ToolCalls) == 0 {
			// Branch on the actual stop reason (resp.Truncated() is true only for
			// max_tokens/length stops). A turn cut off at the output token cap
			// before it emitted any tool call is NOT a voluntary yield — the model
			// ran out of budget mid-thought (commonly the whole allowance went to
			// an extended-thinking block). Treating it as a finish surfaces an
			// empty report and leaves the caller (e.g. the coordinator) puzzled
			// that nothing happened. Instead, nudge the model to continue, bounded
			// by maxTruncRetries.
			if truncated {
				if truncRetries >= maxTruncRetries {
					return &Result{Report: msg.Content, Turns: turn, Truncated: true},
						fmt.Errorf("turn %d truncated at the output token cap with no tool call (after %d retries); raise max_tokens or reduce thinking", turn, truncRetries)
				}
				truncRetries++
				// Keep a SANITIZED copy of the truncated turn in history: drop its
				// thinking blocks (a cut-off block is unsigned and Anthropic rejects
				// it on the next request) and guarantee non-empty content (empty
				// assistant messages are also rejected). Keeping an assistant turn
				// preserves user/assistant alternation, so the follow-up user nudge
				// doesn't collide with the preceding user message.
				stub := gollama.Message{Role: msg.Role, Content: msg.Content}
				if strings.TrimSpace(stub.Content) == "" {
					stub.Content = truncatedStubContent
				}
				l.history = append(l.history, stub)
				l.Post(truncationNudge)
				continue
			}
			// A non-truncated no-tool-call turn is a genuine end-of-turn yield (or
			// an odd stop like refusal): treat its text as the result. msg.Content
			// is guaranteed non-empty here — the pre-emit guard above synthesized a
			// concise stop-reason description for the empty-content case — so the
			// report is never blank and history never holds an empty assistant turn.
			l.history = append(l.history, msg)
			return &Result{Report: msg.Content, Turns: turn, NoContent: noContentYield}, nil
		}
		truncRetries = 0
		// Record the assistant turn (text + tool_use) so context carries forward.
		l.history = append(l.history, msg)

		for ci, call := range msg.ToolCalls {
			l.Emitter.Emit(event.ToolCall, map[string]any{
				"name": call.Function.Name,
				"args": call.Function.Arguments,
				"id":   call.ID,
			})
			toolStart := time.Now()
			res := l.Tools.Dispatch(ctx, call)
			toolMS := time.Since(toolStart).Milliseconds()
			resultData := map[string]any{
				"name":        call.Function.Name,
				"result":      res.Content,
				"error":       res.IsError,
				"images":      len(res.Images),
				"docs":        len(res.Documents),
				"id":          call.ID,
				"duration_ms": toolMS,
			}
			// A display tool may attach a structured view for rich UI rendering
			// (LSP-style trees); it serializes into the event data under "view".
			if v := tools.ViewOf(res); v != nil {
				resultData["view"] = v
			}
			l.Emitter.Emit(event.ToolResult, resultData)
			l.appendToolResult(call.ID, res)
			if ctrl := tools.ControlOf(res); ctrl != nil && ctrl.Stop {
				// The model may batch a control-stop tool (e.g. finish) alongside
				// other tool calls in the SAME turn. Those later calls are never
				// dispatched, so they have no tool_result. If the session later
				// continues on this history (e.g. a user prod after the coordinator
				// finishes), Anthropic rejects an assistant tool_use with no matching
				// tool_result ("messages: ... tool_use ids were found without
				// tool_result blocks"). Append synthetic results for the undispatched
				// remainder so the history stays valid to replay/continue.
				for _, rem := range msg.ToolCalls[ci+1:] {
					l.appendToolResult(rem.ID, &gollama.ToolResult{
						Content: "(skipped: the session ended before this tool ran)",
					})
				}
				report := ctrl.Report
				if report == "" {
					report = msg.Content
				}
				return &Result{Report: report, Turns: turn, NextMode: ctrl.Mode, NextPrompt: ctrl.Prompt}, nil
			}

			// Safe checkpoint after a tool result: pause-to-steer if requested
			// (spec §18.7). A steered correction lands before the next turn.
			if err := l.steerCheckpoint(ctx); err != nil {
				return nil, err
			}
		}
	}

	return &Result{Report: "(stopped: reached max turns)", Turns: maxTurns}, fmt.Errorf("reached max turns (%d)", maxTurns)
}
