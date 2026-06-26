// Package engine implements the core agent loop (spec §7.2): run a model turn,
// dispatch any tool calls, feed results back, and repeat until the model yields
// with no tool calls or a control tool signals stop.
package engine

import (
	"context"
	"errors"
	"fmt"

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

	history []gollama.Message
}

const defaultMaxTurns = 40

// Result is the outcome of a completed loop.
type Result struct {
	Report string // final report (from a control tool) or last assistant text
	Turns  int
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

	for turn := 1; turn <= maxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		opts := gollama.RequestOptions{
			Model:    l.Model,
			System:   l.System,
			Messages: l.history,
			Tools:    l.Tools.APIDefs(),
		}
		if l.MaxTok > 0 {
			opts.Options = &gollama.Options{MaxTokens: l.MaxTok}
		}

		resp, err := l.Client.Turn(opts)
		if err != nil {
			l.Emitter.Emit(event.SessionError, map[string]any{"msg": err.Error()})
			return nil, fmt.Errorf("turn %d: %w", turn, err)
		}
		if len(resp.Choices) == 0 {
			return nil, errors.New("model returned no choices")
		}
		msg := resp.Choices[0].Message

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
				return &Result{Report: report, Turns: turn}, nil
			}
		}
	}

	return &Result{Report: "(stopped: reached max turns)", Turns: maxTurns}, fmt.Errorf("reached max turns (%d)", maxTurns)
}
