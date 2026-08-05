package anthropicauth

import (
	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/engine"
)

const (
	// BillingSystemPrefix is the reserved first system block recognized by
	// Anthropic's Claude subscription inference surface. The entrypoint is ycc,
	// not sdk-cli/Claude Code: this truthfully identifies the caller while using
	// the provider's required classification syntax. The version is a protocol
	// placeholder rather than a claim to be a Claude Code release.
	BillingSystemPrefix = "x-anthropic-billing-header: cc_version=0.0.0; cc_entrypoint=ycc;"

	// AgentSystemPrefix is the reserved SDK identity block Claude Code keeps even
	// when --system-prompt replaces its behavioral instructions. Anthropic
	// subscription inference accepts BillingSystemPrefix by itself; retaining this
	// second observed block makes ycc match the complete reserved prefix the user
	// requested, while ycc's actual behavioral prompt remains a separate block.
	AgentSystemPrefix = "You are a Claude agent, built on Anthropic's Claude Agent SDK."
)

// PrefixSystem returns a copy of opts whose system prompt begins with the
// reserved Anthropic subscription prefix. Existing SystemBlocks retain their
// order after the prefix; a plain System string becomes the final cached block.
// Calling it twice is idempotent, which protects wrapper composition.
func PrefixSystem(opts gollama.RequestOptions) gollama.RequestOptions {
	if len(opts.SystemBlocks) >= 2 && opts.SystemBlocks[0].Text == BillingSystemPrefix && opts.SystemBlocks[1].Text == AgentSystemPrefix {
		return opts
	}
	blocks := make([]gollama.SystemBlock, 0, len(opts.SystemBlocks)+3)
	blocks = append(blocks,
		gollama.SystemBlock{Text: BillingSystemPrefix},
		gollama.SystemBlock{Text: AgentSystemPrefix},
	)
	if len(opts.SystemBlocks) > 0 {
		blocks = append(blocks, opts.SystemBlocks...)
	} else if opts.System != "" {
		blocks = append(blocks, gollama.SystemBlock{Text: opts.System, Cache: true})
	}
	opts.System = ""
	opts.SystemBlocks = blocks
	return opts
}

// Turner decorates the native Anthropic client so both streaming and
// non-streaming turns carry the reserved subscription system prefix. API-key
// clients are never wrapped by config.Registry.Build.
type Turner struct {
	inner engine.Turner
}

func NewTurner(inner engine.Turner) *Turner { return &Turner{inner: inner} }

func (t *Turner) Turn(opts gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	return t.inner.Turn(PrefixSystem(opts))
}

func (t *Turner) TurnStream(opts gollama.RequestOptions, onDelta func(string)) (*gollama.ResponseMessageGenerate, error) {
	if stream, ok := t.inner.(engine.StreamTurner); ok {
		return stream.TurnStream(PrefixSystem(opts), onDelta)
	}
	return t.Turn(opts)
}
