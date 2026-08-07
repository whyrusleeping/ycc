package codex

import (
	"encoding/json"
	"strings"

	"github.com/whyrusleeping/gollama"
)

const itemsBlockMarker = "codex-response-items-v1:"

// SummaryPart is a provider-authored summary entry attached to a Responses API
// reasoning item. It is provider state, not assistant transcript text.
type SummaryPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ResponseItem is the portion of a Codex Responses output item that must be
// echoed on a later store:false request. Message text and function arguments
// deliberately remain in the engine's canonical history; their item IDs are
// retained here so the canonical data can be put back into the original output
// order.
type ResponseItem struct {
	Type             string        `json:"type"`
	ID               string        `json:"id,omitempty"`
	EncryptedContent string        `json:"encrypted_content,omitempty"`
	Summary          []SummaryPart `json:"summary,omitempty"`
	CallID           string        `json:"call_id,omitempty"`
}

type itemsPayload struct {
	Model string         `json:"model"`
	Items []ResponseItem `json:"items"`
}

// EncodeItemsBlock packages Codex's opaque stateless Responses output in a
// ThinkingBlock. ThinkingBlocks are the engine's existing opaque
// provider-state carrier: they are persisted on model_turn.thinking_blocks and
// reconstructed by ReplayHistory. The marker prevents this private payload
// from being treated as visible reasoning or as an Anthropic thinking block.
func EncodeItemsBlock(model string, items []ResponseItem) gollama.ThinkingBlock {
	data, err := json.Marshal(itemsPayload{Model: model, Items: items})
	if err != nil {
		// All payload fields are JSON primitives, so this is unreachable. Return a
		// marked empty payload rather than silently converting provider state into
		// transcript text if that invariant ever changes.
		data = []byte(`{"model":"","items":[]}`)
	}
	return gollama.ThinkingBlock{Redacted: itemsBlockMarker + string(data)}
}

// IsItemsBlock reports whether b carries Codex Responses provider state.
func IsItemsBlock(b gollama.ThinkingBlock) bool {
	return strings.HasPrefix(b.Redacted, itemsBlockMarker)
}

// DecodeItemsBlock decodes a marked Codex provider-state block. ok is false for
// foreign blocks and malformed payloads.
func DecodeItemsBlock(b gollama.ThinkingBlock) (model string, items []ResponseItem, ok bool) {
	if !IsItemsBlock(b) {
		return "", nil, false
	}
	var payload itemsPayload
	if err := json.Unmarshal([]byte(strings.TrimPrefix(b.Redacted, itemsBlockMarker)), &payload); err != nil {
		return "", nil, false
	}
	return payload.Model, payload.Items, true
}
