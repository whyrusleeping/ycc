package engine

import (
	"strings"

	"github.com/whyrusleeping/gollama"
)

// IsContextLengthError reports whether err is a backend "context window
// exceeded" failure — i.e. the conversation history (system + messages) is too
// large for the model, not a transient or output-truncation problem.
//
// gollama surfaces these as HTTP 400 errors whose bodies carry provider-specific
// text (Anthropic: "prompt is too long: N tokens > M maximum"; OpenAI-compatible:
// "context_length_exceeded" / "maximum context length is N tokens"). We match on
// those real context-window signatures only. We deliberately do NOT match generic
// "max_tokens"/output-truncation phrasing — output truncation is a distinct,
// recoverable condition handled in loop.go (see maxTruncRetries).
func IsContextLengthError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	for _, sig := range []string{
		"prompt is too long",                // Anthropic
		"context_length_exceeded",           // OpenAI-compatible error code
		"maximum context length",            // OpenAI-compatible message
		"reduce the length of the messages", // OpenAI-compatible hint
		"context window",                    // generic
		"too many total text bytes",         // some gateways
	} {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

// approxContextTokens returns a coarse, backend-agnostic estimate of the size of
// the conversation that would be sent for a turn (system prompt + messages). It
// sums the byte length of the system prompt and of each message's text content
// (Content plus any MultiContent text blocks) and divides by 4, using the common
// ~4-chars-per-token heuristic. It is APPROXIMATE — it ignores tokenizer
// specifics and does not count images/documents — and is meant only to make
// history growth visible in telemetry, not to enforce an exact budget.
func approxContextTokens(system string, msgs []gollama.Message) int {
	bytes := len(system)
	for _, m := range msgs {
		bytes += len(m.Content)
		for _, b := range m.MultiContent {
			if b.Type == "text" {
				bytes += len(b.Text)
			}
		}
	}
	return bytes / 4
}
