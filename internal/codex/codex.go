// Package codex implements the backend transport for ChatGPT subscription
// (Plus/Pro) inference (spec §13): OpenAI's Codex Responses backend at
// https://chatgpt.com/backend-api/codex/responses. Subscription tokens are
// not valid on the regular platform API, and the codex backend speaks the
// Responses API rather than /chat/completions, so this package provides a
// dedicated engine.Turner/StreamTurner instead of reusing gollama's OpenAI
// client. It translates gollama.RequestOptions (the engine's lingua franca)
// into the codex request shape and folds the SSE stream back into a
// gollama.ResponseMessageGenerate.
//
// Backend quirks handled here (mirroring the official codex CLI):
//   - streaming only (stream:true is forced; Turn accumulates the stream)
//   - store:false is required, with the full input resent every turn
//   - a non-empty top-level instructions field is mandatory
//   - required headers: Authorization bearer, chatgpt-account-id,
//     originator, OpenAI-Beta: responses=experimental
//
// Errors are formatted "API returned non-200 status code NNN: body" to match
// gollama's error strings, so engine.ClassifyAPIError (and with it retry and
// session_error classification) works unchanged.
package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/whyrusleeping/gollama"
)

// DefaultBaseURL is the ChatGPT-backed Codex Responses endpoint ("/responses"
// is appended on the wire).
const DefaultBaseURL = "https://chatgpt.com/backend-api/codex"

// Models lists the model ids the codex backend serves (OAuth-eligible ids
// only — the platform-API catalog does not apply). There is no listing
// endpoint, so this is the curated suggestion set (verified live 2026-07);
// free-text ids still work.
var Models = []string{"gpt-5.6-sol", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini"}

// TokenSource supplies a live access token + ChatGPT account id per request
// (openaiauth.AccessToken in production; injectable for tests).
type TokenSource func(ctx context.Context) (token, accountID string, err error)

// Client is a codex-backend LLM client. It is cheap to construct (one per
// engine Build call) and safe for sequential use by one loop.
type Client struct {
	baseURL    string
	tokens     TokenSource
	httpClient *http.Client
	originator string
}

// New constructs a codex client. baseURL "" means DefaultBaseURL; a trailing
// "/responses" is accepted and normalized away.
func New(baseURL string, tokens TokenSource) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	baseURL = strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/responses")
	return &Client{
		baseURL:    baseURL,
		tokens:     tokens,
		httpClient: &http.Client{Timeout: 15 * time.Minute},
		originator: "ycc",
	}
}

// --- request shape ---

// inputItem is one Responses-API input list entry. Exactly one "shape" is
// populated depending on Type ("message", "function_call",
// "function_call_output").
type inputItem struct {
	Type string `json:"type"`
	// message
	Role    string         `json:"role,omitempty"`
	Content []contentBlock `json:"content,omitempty"`
	// function_call
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	// function_call_output
	Output string `json:"output,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"` // input_text (user/developer) | output_text (assistant)
	Text string `json:"text"`
}

// toolDef is a Responses-API function tool (flat, unlike chat-completions'
// nested {type, function:{...}} shape).
type toolDef struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Strict      bool   `json:"strict"`
	Parameters  any    `json:"parameters"`
}

type reasoningOpts struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type request struct {
	Model             string         `json:"model"`
	Instructions      string         `json:"instructions"`
	Input             []inputItem    `json:"input"`
	Tools             []toolDef      `json:"tools,omitempty"`
	ToolChoice        string         `json:"tool_choice,omitempty"`
	ParallelToolCalls bool           `json:"parallel_tool_calls"`
	Store             bool           `json:"store"`
	Stream            bool           `json:"stream"`
	Reasoning         *reasoningOpts `json:"reasoning,omitempty"`
}

// buildRequest translates gollama.RequestOptions into the codex request body.
func buildRequest(opts gollama.RequestOptions) request {
	req := request{
		Model:             opts.Model,
		Instructions:      opts.System,
		Input:             buildInput(opts.Messages),
		ToolChoice:        "auto",
		ParallelToolCalls: false,
		Store:             false,
		Stream:            true,
	}
	// The backend rejects an empty instructions field.
	if strings.TrimSpace(req.Instructions) == "" {
		req.Instructions = "You are a helpful coding assistant."
	}
	for _, t := range opts.Tools {
		if t.Function == nil {
			continue
		}
		req.Tools = append(req.Tools, toolDef{
			Type:        "function",
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Strict:      false,
			Parameters:  t.Function.Parameters,
		})
	}
	if len(req.Tools) == 0 {
		req.ToolChoice = ""
	}
	// Reasoning effort: same levels as the platform Responses API, with the
	// engine's "max" clamped to xhigh (mirrors gollama's OpenAI mapping).
	// Thinking "off" (loop passes empty Thinking + empty Effort) omits the
	// block entirely; codex models then use their default effort.
	if opts.Effort != "" {
		effort := opts.Effort
		if effort == "max" {
			effort = "xhigh"
		}
		req.Reasoning = &reasoningOpts{Effort: effort, Summary: "auto"}
	}
	return req
}

// buildInput converts engine history into Responses input items. Assistant
// tool calls become function_call items; tool results (role "tool") become
// function_call_output items keyed by the same call_id.
func buildInput(msgs []gollama.Message) []inputItem {
	items := make([]inputItem, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "tool":
			items = append(items, inputItem{
				Type:   "function_call_output",
				CallID: m.ToolCallID,
				Output: m.Content,
			})
		case "assistant":
			if m.Content != "" {
				items = append(items, inputItem{
					Type: "message", Role: "assistant",
					Content: []contentBlock{{Type: "output_text", Text: m.Content}},
				})
			}
			for _, tc := range m.ToolCalls {
				items = append(items, inputItem{
					Type:      "function_call",
					CallID:    tc.ID,
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				})
			}
		default: // user (and any stray system messages ride along as developer)
			role := m.Role
			if role == "system" {
				role = "developer"
			}
			items = append(items, inputItem{
				Type: "message", Role: role,
				Content: []contentBlock{{Type: "input_text", Text: m.Content}},
			})
		}
	}
	return items
}

// --- SSE response handling ---

// sseEvent mirrors the fields we consume across codex stream event types.
type sseEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`
	Item  *struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		CallID    string `json:"call_id"`
		Content   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"item"`
	Response *struct {
		Status            string `json:"status"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Usage *struct {
			InputTokens        int `json:"input_tokens"`
			InputTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			OutputTokens        int `json:"output_tokens"`
			OutputTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	} `json:"response"`
}

// Turn runs one model turn against the codex backend. The backend is
// streaming-only, so Turn accumulates the stream silently.
func (c *Client) Turn(opts gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	return c.TurnStream(opts, nil)
}

// TurnStream runs one model turn, invoking onDelta with a snapshot of the full
// accumulated output text after each fragment arrives (nil onDelta = accumulate
// silently). Snapshot semantics satisfy engine.StreamTurner's contract and let
// lossy clients replace their live tail rather than having to retain every delta.
func (c *Client) TurnStream(opts gollama.RequestOptions, onDelta func(text string)) (*gollama.ResponseMessageGenerate, error) {
	ctx := context.Background()
	tok, accountID, err := c.tokens(ctx)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(buildRequest(opts))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+tok)
	if accountID != "" {
		req.Header.Set("chatgpt-account-id", accountID)
	}
	req.Header.Set("originator", c.originator)
	req.Header.Set("OpenAI-Beta", "responses=experimental")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		// Format matches gollama's http.go so engine.ClassifyAPIError parses
		// the status ("status code (\d+)") identically across backends.
		return nil, fmt.Errorf("API returned non-200 status code %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return parseStream(resp.Body, opts.Model, onDelta)
}

// parseStream folds the SSE event stream into a single response message.
func parseStream(r io.Reader, model string, onDelta func(string)) (*gollama.ResponseMessageGenerate, error) {
	var (
		text      strings.Builder
		thinking  strings.Builder
		toolCalls []gollama.ToolCall
		out       = &gollama.ResponseMessageGenerate{Model: model, Done: true, StopReason: "stop"}
		completed bool
	)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var ev sseEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue // tolerate unknown/partial frames
		}
		switch ev.Type {
		case "response.output_text.delta":
			text.WriteString(ev.Delta)
			if onDelta != nil {
				onDelta(text.String())
			}
		case "response.reasoning_summary_text.delta":
			thinking.WriteString(ev.Delta)
		case "response.output_item.done":
			if ev.Item == nil {
				continue
			}
			switch ev.Item.Type {
			case "function_call":
				toolCalls = append(toolCalls, gollama.ToolCall{
					ID:   ev.Item.CallID,
					Type: "function",
					Function: gollama.ToolCallFunction{
						Name:      ev.Item.Name,
						Arguments: ev.Item.Arguments,
					},
				})
			case "message":
				// Authoritative final text for the item; prefer it if the
				// delta path produced nothing (e.g. no streaming callbacks).
				if text.Len() == 0 {
					for _, c := range ev.Item.Content {
						if c.Type == "output_text" {
							text.WriteString(c.Text)
						}
					}
				}
			}
		case "response.completed", "response.incomplete":
			completed = true
			if ev.Response == nil {
				continue
			}
			if ev.Response.Usage != nil {
				u := ev.Response.Usage
				out.Usage = gollama.Usage{
					PromptTokens:     u.InputTokens,
					CompletionTokens: u.OutputTokens,
					TotalTokens:      u.TotalTokens,
					PromptTokensDetails: &gollama.PromptTokensDetails{
						CachedTokens: u.InputTokensDetails.CachedTokens,
					},
				}
			}
			if ev.Response.IncompleteDetails != nil && ev.Response.IncompleteDetails.Reason == "max_output_tokens" {
				out.StopReason = "length"
			}
		case "response.failed":
			msg := "response failed"
			if ev.Response != nil && ev.Response.Error != nil {
				msg = ev.Response.Error.Message
			}
			return nil, fmt.Errorf("codex: %s", msg)
		case "error":
			return nil, fmt.Errorf("codex: stream error: %s", data)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("codex: reading stream: %w", err)
	}
	if !completed && text.Len() == 0 && len(toolCalls) == 0 {
		return nil, fmt.Errorf("codex: stream ended without a completed response")
	}
	if len(toolCalls) > 0 {
		out.StopReason = "tool_calls"
	}
	out.Choices = []gollama.GenChoice{{
		Message: gollama.Message{
			Role:      "assistant",
			Content:   text.String(),
			Thinking:  thinking.String(),
			ToolCalls: toolCalls,
		},
		FinishReason: out.StopReason,
	}}
	return out, nil
}
