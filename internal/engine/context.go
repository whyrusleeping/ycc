package engine

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"strings"

	"github.com/whyrusleeping/gollama"
	_ "golang.org/x/image/webp"
)

// IsContextLengthError reports whether err is a backend "context window
// exceeded" failure — i.e. the conversation history (system + messages) is too
// large for the model, not a transient or output-truncation problem.
//
// gollama surfaces these as HTTP 400 errors whose bodies carry provider-specific
// text (Anthropic: "prompt is too long: N tokens > M maximum"; OpenAI-compatible:
// "context_length_exceeded" / "maximum context length is N tokens"). Detection
// lives in the shared classifier (apierror.go, contextLengthSignatures); this is
// a convenience predicate over it.
func IsContextLengthError(err error) bool {
	return ClassifyAPIError(err).Kind == KindContextLength
}

const (
	contextCharsPerToken = 4
	remoteMediaFallback  = 256
	codexRequestShape    = "codex-responses"
)

// contextEstimate is an explicitly approximate estimate of provider input for
// one request. MediaUncertain is true whenever the request contains media: image
// and document tokenization depends on provider, model, dimensions/pages, and
// detail settings and cannot be recovered exactly from the generic request.
type contextEstimate struct {
	Tokens         int
	RawTokens      int
	MediaTokens    int
	MediaUncertain bool
}

// contextRequestShapeReporter lets a specialized Turner identify a request
// serializer that differs from its logical backend family. The Codex Responses
// transport uses this to distinguish its replay items from OpenAI chat messages.
type contextRequestShapeReporter interface {
	ContextRequestShape() string
}

func requestShape(client Turner, backend string) string {
	if reporter, ok := client.(contextRequestShapeReporter); ok {
		return reporter.ContextRequestShape()
	}
	return strings.ToLower(strings.TrimSpace(backend))
}

// estimateRequestContext estimates the complete input side of the next request:
// system instructions, tool definitions (on every request), message text, tool
// call arguments/results, and provider replay state accepted by the selected
// serializer. Text and JSON use the tokenizer-independent ~4-bytes/token rule.
// Media uses documented provider-shaped pixel approximations where dimensions
// can be decoded and a byte-size/remote placeholder otherwise; it is always
// flagged uncertain and must not be presented as exact usage.
func estimateRequestContext(opts gollama.RequestOptions, shape string) contextEstimate {
	var textBytes, mediaTokens int
	mediaUncertain := false
	add := func(s string) { textBytes += len(s) }
	addJSON := func(v any) {
		if b, err := json.Marshal(v); err == nil {
			textBytes += len(b)
		}
	}

	if len(opts.SystemBlocks) > 0 {
		for _, block := range opts.SystemBlocks {
			add(block.Text)
		}
	} else {
		add(opts.System)
	}
	// Schemas are transmitted afresh on every stateless request. Marshaling the
	// generic definitions closely tracks both nested Chat Completions/Anthropic
	// and flattened Codex definitions while retaining all schema prose.
	if len(opts.Tools) > 0 {
		addJSON(opts.Tools)
	}

	addCalls := func(calls []gollama.ToolCall) {
		for _, call := range calls {
			add(call.ID)
			add(call.Type)
			add(call.Function.Name)
			add(call.Function.Arguments)
		}
	}
	addImages := func(images []string) {
		for _, data := range images {
			mediaTokens += estimateImageTokens(data, "", shape)
			mediaUncertain = true
		}
	}
	addDocuments := func(documents []gollama.Document) {
		for _, doc := range documents {
			mediaTokens += estimateDocumentTokens(doc.Base64, doc.URL)
			add(doc.Title)
			mediaUncertain = true
		}
	}
	addMultiContent := func(blocks []gollama.ContentBlock, documents bool) {
		for _, block := range blocks {
			switch block.Type {
			case "text":
				add(block.Text)
			case "image":
				mediaTokens += estimateImageTokens(block.ImageBase64, block.ImageURL, shape)
				mediaUncertain = true
			case "document":
				if documents {
					mediaTokens += estimateDocumentTokens(block.DocumentBase64, block.DocumentURL)
					add(block.DocumentTitle)
					mediaUncertain = true
				}
			}
		}
	}

	anthropicShape := shape == "anthropic" || shape == "bedrock"
	codexShape := shape == codexRequestShape
	for _, msg := range opts.Messages {
		if anthropicShape && msg.Role == "system" {
			// Anthropic consumes system-role messages into the top-level system only
			// when no explicit System/SystemBlocks was supplied; otherwise it drops
			// them from messages.
			if opts.System == "" && len(opts.SystemBlocks) == 0 {
				add(msg.Content)
			}
			continue
		}

		add(msg.Role)
		switch {
		case codexShape:
			switch msg.Role {
			case "tool":
				add(msg.Content)
				add(msg.ToolCallID)
			case "assistant":
				add(msg.Content)
				addCalls(msg.ToolCalls)
				if state, ok := codexReplayState(msg.ThinkingBlocks, opts.Model, msg); ok {
					addJSON(state)
				}
			default:
				// buildInput selects MultiContent over Content and ignores legacy
				// Images/Documents entirely.
				if len(msg.MultiContent) > 0 {
					addMultiContent(msg.MultiContent, false)
				} else {
					add(msg.Content)
				}
			}
		case anthropicShape:
			switch msg.Role {
			case "tool":
				add(msg.Content)
				add(msg.ToolCallID)
				addImages(msg.Images)
				addDocuments(msg.Documents)
			case "assistant":
				// Anthropic assistant conversion ignores all user/tool media fields.
				add(msg.Content)
				addCalls(msg.ToolCalls)
				for _, block := range msg.ThinkingBlocks {
					add(block.Thinking)
					add(block.Signature)
					add(block.Redacted)
				}
			default:
				// Anthropic user conversion selects MultiContent over Content and
				// legacy Images/Documents.
				if len(msg.MultiContent) > 0 {
					addMultiContent(msg.MultiContent, true)
				} else {
					add(msg.Content)
					addImages(msg.Images)
					addDocuments(msg.Documents)
				}
			}
		default: // OpenAI-compatible and Ollama chat serializers.
			switch {
			case msg.Role != "tool" && len(msg.MultiContent) > 0 && !msg.UseAnthropicFormat:
				// Message.MarshalJSON selects MultiContent and tool calls only.
				addMultiContent(msg.MultiContent, false)
				addCalls(msg.ToolCalls)
			case msg.Role != "tool" && len(msg.Images) > 0:
				// Legacy image conversion keeps Content and tool calls but drops the
				// reasoning and tool-result fields from its custom object.
				add(msg.Content)
				addCalls(msg.ToolCalls)
				addImages(msg.Images)
			default:
				add(msg.Content)
				add(msg.Thinking)
				add(msg.ReasoningContent)
				add(msg.Reasoning)
				add(msg.ToolCallID)
				addCalls(msg.ToolCalls)
				addImages(msg.Images)
			}
		}
	}

	tokens := (textBytes + contextCharsPerToken - 1) / contextCharsPerToken
	tokens += mediaTokens
	return contextEstimate{Tokens: tokens, RawTokens: tokens, MediaTokens: mediaTokens, MediaUncertain: mediaUncertain}
}

type codexEstimateItem struct {
	Type             string `json:"type"`
	ID               string `json:"id,omitempty"`
	EncryptedContent string `json:"encrypted_content,omitempty"`
	Summary          []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary,omitempty"`
}

// codexReplayState mirrors the state-selection part of buildAssistantItems: it
// uses only the first valid same-model block, omits unsupported/unmatched items,
// and removes reasoning that would not have a following canonical message or
// function call in the submitted assistant turn.
func codexReplayState(blocks []gollama.ThinkingBlock, model string, msg gollama.Message) (any, bool) {
	var recorded []codexEstimateItem
	found := false
	for _, block := range blocks {
		if !strings.HasPrefix(block.Redacted, codexItemsBlockMarker) {
			continue
		}
		var payload struct {
			Model string              `json:"model"`
			Items []codexEstimateItem `json:"items"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(block.Redacted, codexItemsBlockMarker)), &payload); err != nil || payload.Model != model {
			continue
		}
		recorded, found = payload.Items, true
		break
	}
	if !found {
		return nil, false
	}

	messageConsumed := false
	toolIndex := 0
	kept := make([]codexEstimateItem, 0, len(recorded))
	for _, item := range recorded {
		switch item.Type {
		case "reasoning":
			if item.Summary == nil {
				item.Summary = []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}{}
			}
			kept = append(kept, item)
		case "message":
			if messageConsumed || msg.Content == "" {
				continue
			}
			kept = append(kept, codexEstimateItem{Type: item.Type, ID: item.ID})
			messageConsumed = true
		case "function_call":
			if toolIndex >= len(msg.ToolCalls) {
				continue
			}
			kept = append(kept, codexEstimateItem{Type: item.Type, ID: item.ID})
			toolIndex++
		}
	}

	// buildAssistantItems appends canonical content and any leftover tool calls
	// after the recorded-item pass. Either is therefore a valid following item
	// for trailing recorded reasoning.
	hasFollowing := (!messageConsumed && msg.Content != "") || toolIndex < len(msg.ToolCalls)
	for i := len(kept) - 1; i >= 0; i-- {
		switch kept[i].Type {
		case "message", "function_call":
			hasFollowing = true
		case "reasoning":
			if !hasFollowing {
				kept = append(kept[:i], kept[i+1:]...)
			}
		}
	}
	if len(kept) == 0 {
		return nil, false
	}
	return kept, true
}

func estimateImageTokens(data, url, shape string) int {
	if data == "" {
		if url == "" {
			return 0
		}
		// Remote dimensions and detail are unavailable until the provider fetches
		// the URL. This placeholder is deliberately exposed as uncertain.
		return remoteMediaFallback + (len(url)+contextCharsPerToken-1)/contextCharsPerToken
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err == nil {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(decoded)); err == nil && cfg.Width > 0 && cfg.Height > 0 {
			pixels := cfg.Width * cfg.Height
			if shape == "openai" || shape == codexRequestShape {
				// Rough high-detail tile proxy. The actual OpenAI/Codex cost is
				// model- and detail-dependent (the request leaves detail on auto).
				tiles := ((cfg.Width + 511) / 512) * ((cfg.Height + 511) / 512)
				return 85 + 170*tiles
			}
			// Anthropic publishes approximately one token per 750 pixels after
			// provider-side resizing. Other vision backends use this only as a proxy.
			return max(1, (pixels+749)/750)
		}
		return max(1, (len(decoded)+749)/750)
	}
	return max(1, (len(data)+999)/1000)
}

func estimateDocumentTokens(data, url string) int {
	if data == "" {
		if url == "" {
			return 0
		}
		return remoteMediaFallback + (len(url)+contextCharsPerToken-1)/contextCharsPerToken
	}
	if decoded, err := base64.StdEncoding.DecodeString(data); err == nil {
		return max(1, (len(decoded)+749)/750)
	}
	return max(1, (len(data)+999)/1000)
}

// measuredProviderInput returns the provider's measured total input for a
// completed request. Anthropic reports fresh/cache-read/cache-write input as
// separate classes; OpenAI includes cached input in PromptTokens. A zero result
// means the legacy/provider response did not supply an input measurement.
func measuredProviderInput(u gollama.Usage) int {
	if u.CacheReadInputTokens > 0 || u.CacheCreationInputTokens > 0 {
		return u.PromptTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	}
	return u.PromptTokens
}
