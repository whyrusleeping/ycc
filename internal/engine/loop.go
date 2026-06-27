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

// Loop drives one agent (coordinator or subagent) over a backend.
type Loop struct {
	Client   Turner
	Model    string
	System   string
	Tools    *tools.Registry
	Emitter  *event.Emitter
	MaxTurns int // 0 => default
	MaxTok   int // per-turn max tokens; 0 => backend default

	// Anthropic extended/adaptive reasoning (spec §7, §13). Thinking == ""
	// disables reasoning; "adaptive" enables it. Effort tunes depth/spend
	// ("low".."max"); ThinkingDisplay ("summarized") opts into reasoning
	// summaries. Honored by the anthropic backend, ignored by others.
	Thinking        string
	Effort          string
	ThinkingDisplay string

	mu      sync.Mutex // guards Client/Model swaps mid-loop (settings overlay, §18.2)
	history []gollama.Message
}

// SetBackend swaps the loop's backend client, model id, and per-model reasoning
// settings while preserving the conversation history, so a mid-session
// role-config change takes effect on the next turn (spec §18.2). Safe to call
// concurrently with Run.
func (l *Loop) SetBackend(client Turner, model string, think Thinking) {
	l.mu.Lock()
	l.Client = client
	l.Model = model
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

func (l *Loop) backend() (Turner, string, Thinking) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Client, l.Model, Thinking{Thinking: l.Thinking, Effort: l.Effort, ThinkingDisplay: l.ThinkingDisplay}
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

// Result is the outcome of a completed loop.
type Result struct {
	Report     string // final report (from a control tool) or last assistant text
	Turns      int
	NextMode   string // if set, a control tool requested a transition to this mode
	NextPrompt string // if set, the verbatim seed prompt for the next mode's loop
}

// Seed appends an initial user message (the task prompt) before Run.
func (l *Loop) Seed(prompt string) { l.Post(prompt) }

// Post appends a user message to the conversation. Used both to seed the initial
// task and to inject follow-up input between Run calls (a "prod"), so a session
// can continue the same agent across multiple turns.
func (l *Loop) Post(content string) {
	l.history = append(l.history, gollama.Message{Role: "user", Content: content})
}

// Run executes the loop to completion. It returns when a control tool signals
// stop, the model produces a turn with no tool calls, or MaxTurns is reached.
func (l *Loop) Run(ctx context.Context) (*Result, error) {
	maxTurns := l.MaxTurns
	if maxTurns == 0 {
		maxTurns = defaultMaxTurns
	}

	// turn resets to 1 on every Run, so MaxTurns is a per-Run budget rather
	// than a cumulative one across revise rounds (see defaultMaxTurns).
	for turn := 1; turn <= maxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		client, modelID, think := l.backend()
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

		resp, err := client.Turn(opts)
		if err != nil {
			l.Emitter.Emit(event.SessionError, map[string]any{"msg": err.Error()})
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

		l.Emitter.Emit(event.ModelTurn, map[string]any{
			"text":       msg.Content,
			"tool_calls": len(msg.ToolCalls),
		})
		// Record the assistant turn (text + tool_use) so context carries forward.
		l.history = append(l.history, msg)

		if len(msg.ToolCalls) == 0 {
			// Model yielded with no further action: treat its text as the result.
			return &Result{Report: msg.Content, Turns: turn}, nil
		}

		for _, call := range msg.ToolCalls {
			l.Emitter.Emit(event.ToolCall, map[string]any{
				"name": call.Function.Name,
				"args": call.Function.Arguments,
				"id":   call.ID,
			})
			res := l.Tools.Dispatch(ctx, call)
			l.Emitter.Emit(event.ToolResult, map[string]any{
				"name":   call.Function.Name,
				"result": res.Content,
				"error":  res.IsError,
			})
			l.history = append(l.history, gollama.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    res.Content,
			})
			if ctrl := tools.ControlOf(res); ctrl != nil && ctrl.Stop {
				report := ctrl.Report
				if report == "" {
					report = msg.Content
				}
				return &Result{Report: report, Turns: turn, NextMode: ctrl.Mode, NextPrompt: ctrl.Prompt}, nil
			}
		}
	}

	return &Result{Report: "(stopped: reached max turns)", Turns: maxTurns}, fmt.Errorf("reached max turns (%d)", maxTurns)
}
