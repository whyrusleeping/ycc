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
	"sync/atomic"
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
	// reasoningTokens carries the most recently completed turn's hidden
	// reasoning-token count across the gollama transport boundary. A loop consumes
	// it immediately after Turn/TurnStream; atomic keeps the optional capability
	// safe if a caller inspects it concurrently.
	reasoningTokens atomic.Int64
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
	ID   string `json:"id,omitempty"`
	// message
	Role    string         `json:"role,omitempty"`
	Content []contentBlock `json:"content,omitempty"`
	// function_call
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	// function_call_output
	Output string `json:"output,omitempty"`
	// reasoning. Summary is a pointer so an explicitly empty slice marshals as
	// the required [], while the field remains absent from other item types.
	EncryptedContent string         `json:"encrypted_content,omitempty"`
	Summary          *[]SummaryPart `json:"summary,omitempty"`
}

type contentBlock struct {
	Type     string `json:"type"` // input_text | input_image | output_text
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
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

// streamOpts asks the Codex Responses backend to finish each reasoning-summary
// section in sequence. Without this, concurrently generated sections can be cut
// off before their authoritative `...text.done` event arrives, leaving ycc with
// only an early fragment of the provider-visible summary.
type streamOpts struct {
	ReasoningSummaryDelivery string `json:"reasoning_summary_delivery"`
}

type request struct {
	Model             string         `json:"model"`
	MaxOutputTokens   int            `json:"max_output_tokens,omitempty"`
	Instructions      string         `json:"instructions"`
	Input             []inputItem    `json:"input"`
	Tools             []toolDef      `json:"tools,omitempty"`
	ToolChoice        string         `json:"tool_choice,omitempty"`
	ParallelToolCalls bool           `json:"parallel_tool_calls"`
	Store             bool           `json:"store"`
	Stream            bool           `json:"stream"`
	StreamOptions     *streamOpts    `json:"stream_options,omitempty"`
	Reasoning         *reasoningOpts `json:"reasoning,omitempty"`
	Include           []string       `json:"include,omitempty"`
}

// buildRequest translates gollama.RequestOptions into the codex request body.
func buildRequest(opts gollama.RequestOptions) request {
	req := request{
		Model:             opts.Model,
		Instructions:      opts.System,
		Input:             buildInput(opts.Messages, opts.Model),
		ToolChoice:        "auto",
		ParallelToolCalls: false,
		Store:             false,
		Stream:            true,
		Include:           []string{"reasoning.encrypted_content"},
	}
	// The backend rejects an empty instructions field.
	if strings.TrimSpace(req.Instructions) == "" {
		req.Instructions = "You are a helpful coding assistant."
	}
	if opts.Options != nil && opts.Options.MaxTokens > 0 {
		req.MaxOutputTokens = opts.Options.MaxTokens
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
		// "detailed" affects only the user-visible summary, not the private
		// reasoning itself. It gives ycc the most useful trace the provider makes
		// available. Sequential delivery makes completed sections authoritative and
		// prevents later summary work from truncating earlier sections.
		req.Reasoning = &reasoningOpts{Effort: effort, Summary: "detailed"}
		req.StreamOptions = &streamOpts{ReasoningSummaryDelivery: "sequential_cutoff"}
	}
	return req
}

// buildInput converts engine history into Responses input items. Assistant
// tool calls become function_call items; tool results (role "tool") become
// function_call_output items keyed by the same call_id. When an assistant turn
// carries a Codex items block, its opaque reasoning and provider item ordering
// are restored while visible text and tool data still come from canonical
// engine history.
func buildInput(msgs []gollama.Message, model string) []inputItem {
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
			items = append(items, buildAssistantItems(m, model)...)
		default: // user (and any stray system messages ride along as developer)
			role := m.Role
			if role == "system" {
				role = "developer"
			}
			content := []contentBlock{}
			if len(m.MultiContent) > 0 {
				for _, block := range m.MultiContent {
					switch block.Type {
					case "text":
						content = append(content, contentBlock{Type: "input_text", Text: block.Text})
					case "image":
						if block.ImageURL != "" {
							content = append(content, contentBlock{Type: "input_image", ImageURL: block.ImageURL})
						} else if block.ImageBase64 != "" {
							mediaType := block.ImageMediaType
							if mediaType == "" {
								mediaType = "image/jpeg"
							}
							content = append(content, contentBlock{Type: "input_image", ImageURL: "data:" + mediaType + ";base64," + block.ImageBase64})
						}
					}
				}
			} else {
				content = append(content, contentBlock{Type: "input_text", Text: m.Content})
			}
			items = append(items, inputItem{Type: "message", Role: role, Content: content})
		}
	}
	return items
}

func buildAssistantItems(m gollama.Message, model string) []inputItem {
	var recorded []ResponseItem
	for _, block := range m.ThinkingBlocks {
		recordedModel, candidate, ok := DecodeItemsBlock(block)
		if ok && recordedModel == model {
			recorded = candidate
			break
		}
	}

	messageConsumed := false
	toolIndex := 0
	items := make([]inputItem, 0, len(recorded)+len(m.ToolCalls)+1)
	for _, item := range recorded {
		switch item.Type {
		case "reasoning":
			summary := item.Summary
			if summary == nil {
				summary = []SummaryPart{}
			}
			items = append(items, inputItem{
				Type:             "reasoning",
				ID:               item.ID,
				EncryptedContent: item.EncryptedContent,
				Summary:          &summary,
			})
		case "message":
			if messageConsumed || m.Content == "" {
				continue
			}
			items = append(items, inputItem{
				Type: "message", ID: item.ID, Role: "assistant",
				Content: []contentBlock{{Type: "output_text", Text: m.Content}},
			})
			messageConsumed = true
		case "function_call":
			if toolIndex >= len(m.ToolCalls) {
				continue
			}
			tc := m.ToolCalls[toolIndex]
			toolIndex++
			items = append(items, inputItem{
				Type:      "function_call",
				ID:        item.ID,
				CallID:    tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}
	}
	if !messageConsumed && m.Content != "" {
		items = append(items, inputItem{
			Type: "message", Role: "assistant",
			Content: []contentBlock{{Type: "output_text", Text: m.Content}},
		})
	}
	for ; toolIndex < len(m.ToolCalls); toolIndex++ {
		tc := m.ToolCalls[toolIndex]
		items = append(items, inputItem{
			Type:      "function_call",
			CallID:    tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}

	// The Responses API rejects a reasoning item without its required following
	// message/function_call. Work backwards so each kept reasoning item has such
	// an item later in this same assistant turn.
	hasFollowing := false
	for i := len(items) - 1; i >= 0; i-- {
		switch items[i].Type {
		case "message", "function_call":
			hasFollowing = true
		case "reasoning":
			if !hasFollowing {
				items = append(items[:i], items[i+1:]...)
			}
		}
	}
	return items
}

// --- SSE response handling ---

// sseEvent mirrors the fields we consume across codex stream event types.
type sseEvent struct {
	Type         string `json:"type"`
	Delta        string `json:"delta"`
	Text         string `json:"text"`
	SummaryIndex *int   `json:"summary_index"`
	ItemID       string `json:"item_id"`
	Item         *struct {
		ID               string `json:"id"`
		Type             string `json:"type"`
		Name             string `json:"name"`
		Arguments        string `json:"arguments"`
		CallID           string `json:"call_id"`
		EncryptedContent string `json:"encrypted_content"`
		Content          []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Summary []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"summary"`
	} `json:"item"`
	// Error is the payload of a stream-level `error` frame, which the backend
	// can send over an otherwise-healthy HTTP 200 stream. It is distinct from
	// Response.Error (carried by `response.failed`).
	Error *struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
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

// ReasoningTokens returns the hidden reasoning tokens reported for the most
// recently completed turn. engine.Loop reads this optional capability directly
// after the turn, because gollama.Usage does not currently expose output-token
// details.
func (c *Client) ReasoningTokens() int { return int(c.reasoningTokens.Load()) }

// TurnCtx runs one model turn against the codex backend. The backend is
// streaming-only, so TurnCtx accumulates the stream silently.
func (c *Client) TurnCtx(ctx context.Context, opts gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	return c.TurnStreamCtx(ctx, opts, nil)
}

// TurnStreamCtx runs one model turn, invoking onDelta with a snapshot of the full
// accumulated output text after each fragment arrives (nil onDelta = accumulate
// silently). Snapshot semantics satisfy engine.StreamTurner's contract and let
// lossy clients replace their live tail rather than having to retain every delta.
func (c *Client) TurnStreamCtx(ctx context.Context, opts gollama.RequestOptions, onDelta func(text string)) (*gollama.ResponseMessageGenerate, error) {
	// Never leak a previous successful turn's count through an errored turn.
	c.reasoningTokens.Store(0)
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
	result, reasoningTokens, err := parseStream(resp.Body, opts.Model, onDelta)
	if err == nil {
		c.reasoningTokens.Store(int64(reasoningTokens))
	}
	return result, err
}

// parseStream folds the SSE event stream into a single response message and
// returns the provider-reported hidden reasoning-token count separately because
// gollama.Usage does not currently model output_tokens_details.
func parseStream(r io.Reader, model string, onDelta func(string)) (*gollama.ResponseMessageGenerate, int, error) {
	type summaryPart struct {
		itemID string
		index  int
		text   string
	}
	var (
		text            strings.Builder
		legacyThinking  strings.Builder
		summaryParts    []*summaryPart
		toolCalls       []gollama.ToolCall
		responseItems   []ResponseItem
		out             = &gollama.ResponseMessageGenerate{Model: model, Done: true, StopReason: "stop"}
		completed       bool
		reasoningTokens int
	)
	// Summary deltas and done events identify a section by reasoning item +
	// summary index. Some older streams omit item_id, so let a later authoritative
	// event adopt an unclaimed same-index part rather than displaying it twice.
	findSummaryPart := func(itemID string, index int) *summaryPart {
		for _, part := range summaryParts {
			if part.itemID == itemID && part.index == index {
				return part
			}
		}
		if itemID != "" {
			for _, part := range summaryParts {
				if part.itemID == "" && part.index == index {
					part.itemID = itemID
					return part
				}
			}
		}
		part := &summaryPart{itemID: itemID, index: index}
		summaryParts = append(summaryParts, part)
		return part
	}
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
			if ev.SummaryIndex == nil {
				// Compatibility with the original Codex stream shape, before
				// summary sections carried stable indexes.
				legacyThinking.WriteString(ev.Delta)
				continue
			}
			findSummaryPart(ev.ItemID, *ev.SummaryIndex).text += ev.Delta
		case "response.reasoning_summary_text.done":
			if ev.SummaryIndex == nil {
				if ev.Text != "" {
					legacyThinking.Reset()
					legacyThinking.WriteString(ev.Text)
				}
				continue
			}
			// The done event is authoritative: replace (do not append to) the
			// partial deltas, which may have been truncated or lossy.
			findSummaryPart(ev.ItemID, *ev.SummaryIndex).text = ev.Text
		case "response.output_item.done":
			if ev.Item == nil {
				continue
			}
			switch ev.Item.Type {
			case "reasoning":
				// Non-streaming-compatible fallback: a completed reasoning item
				// may carry its summary array without summary text events.
				summaryPartsForReplay := make([]SummaryPart, 0, len(ev.Item.Summary))
				for i, summary := range ev.Item.Summary {
					summaryPartsForReplay = append(summaryPartsForReplay, SummaryPart{Type: summary.Type, Text: summary.Text})
					if summary.Type == "summary_text" && summary.Text != "" {
						findSummaryPart(ev.Item.ID, i).text = summary.Text
					}
				}
				responseItems = append(responseItems, ResponseItem{
					Type:             "reasoning",
					ID:               ev.Item.ID,
					EncryptedContent: ev.Item.EncryptedContent,
					Summary:          summaryPartsForReplay,
				})
			case "function_call":
				responseItems = append(responseItems, ResponseItem{Type: "function_call", ID: ev.Item.ID, CallID: ev.Item.CallID})
				toolCalls = append(toolCalls, gollama.ToolCall{
					ID:   ev.Item.CallID,
					Type: "function",
					Function: gollama.ToolCallFunction{
						Name:      ev.Item.Name,
						Arguments: ev.Item.Arguments,
					},
				})
			case "message":
				responseItems = append(responseItems, ResponseItem{Type: "message", ID: ev.Item.ID})
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
				reasoningTokens = u.OutputTokensDetails.ReasoningTokens
			}
			if ev.Response.IncompleteDetails != nil && ev.Response.IncompleteDetails.Reason == "max_output_tokens" {
				out.StopReason = "length"
			}
		case "response.failed":
			msg := "response failed"
			if ev.Response != nil && ev.Response.Error != nil {
				msg = errorText(ev.Response.Error.Code, "", ev.Response.Error.Message, msg)
			}
			return nil, 0, fmt.Errorf("codex: %s", msg)
		case "error":
			return nil, 0, fmt.Errorf("codex: stream error: %s", streamErrorText(ev, data))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, fmt.Errorf("codex: reading stream: %w", err)
	}
	if !completed && text.Len() == 0 && len(toolCalls) == 0 {
		return nil, 0, fmt.Errorf("codex: stream ended without a completed response")
	}
	if len(toolCalls) > 0 {
		out.StopReason = "tool_calls"
	}
	thinking := make([]string, 0, len(summaryParts)+1)
	if s := strings.TrimSpace(legacyThinking.String()); s != "" {
		thinking = append(thinking, s)
	}
	for _, part := range summaryParts {
		if s := strings.TrimSpace(part.text); s != "" {
			thinking = append(thinking, s)
		}
	}
	var blocks []gollama.ThinkingBlock
	if len(responseItems) > 0 {
		blocks = []gollama.ThinkingBlock{EncodeItemsBlock(model, responseItems)}
	}
	out.Choices = []gollama.GenChoice{{
		Message: gollama.Message{
			Role:           "assistant",
			Content:        text.String(),
			Thinking:       strings.Join(thinking, "\n\n"),
			ThinkingBlocks: blocks,
			ToolCalls:      toolCalls,
		},
		FinishReason: out.StopReason,
	}}
	return out, reasoningTokens, nil
}

// streamErrorText renders a stream-level `error` frame. Such a frame arrives
// over an HTTP 200 stream, so there is no status code for
// engine.ClassifyAPIError to parse; keeping the provider's error CODE in the
// message is what lets the classifier recognise a transient backend failure
// (`server_error`, which the provider's own message tells clients to retry)
// instead of treating it as an unknown — and therefore permanent — error. Falls
// back to the raw frame when the payload is not the documented shape, so
// nothing is ever silently swallowed.
func streamErrorText(ev sseEvent, raw string) string {
	if ev.Error == nil {
		return raw
	}
	return errorText(ev.Error.Code, ev.Error.Type, ev.Error.Message, raw)
}

// errorText joins a provider error code with its message ("code: message"),
// preferring code over type and degrading gracefully to whichever part is
// present, or to fallback when neither is.
func errorText(code, typ, message, fallback string) string {
	if code == "" {
		code = typ
	}
	switch {
	case code != "" && message != "":
		return code + ": " + message
	case code != "":
		return code
	case message != "":
		return message
	}
	return fallback
}
