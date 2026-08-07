package anthropicauth

import (
	"context"
	"strings"
	"sync"
	"time"

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
//
// When constructed with NewOAuthTurner it additionally keeps the transport's
// bearer credential live. Anthropic invalidates the previous access token every
// time the refresh token is redeemed, so a token resolved once when the client
// was built dies the moment anything else (another ycc process, the
// subscription-usage poller, `ycc doctor`) refreshes — killing sessions that
// outlive it with a non-retryable 401. Resolving per turn, plus one forced
// refresh + retry when the provider reports the token revoked, makes a
// long-running session survive a background rotation.
type Turner struct {
	inner engine.Turner
	src   *TokenSource

	mu    sync.Mutex
	using string // the access token currently installed on the transport
}

// TokenSource supplies live OAuth credentials to a Turner and installs them on
// the underlying transport. Fields are injectable so tests do not need the real
// secrets store; production values are AccessToken, ForceRefresh and the
// gollama client's SetBearerToken.
type TokenSource struct {
	// Token returns a currently-valid access token, refreshing a stored one
	// that has expired. It is called once per turn, so it must be cheap when
	// no refresh is due.
	Token func(ctx context.Context) (string, error)
	// Refresh replaces an access token the provider rejected as revoked or
	// expired. It receives the rejected token so it can prefer a credential
	// another process already refreshed over redeeming the refresh token again.
	Refresh func(ctx context.Context, stale string) (string, error)
	// Apply installs an access token as the transport's bearer credential.
	Apply func(token string)
}

// NewTurner wraps a client whose credential is static (a long-lived
// `sk-ant-oat` token stored under key_env): system-prefix behavior only.
func NewTurner(inner engine.Turner) *Turner { return &Turner{inner: inner} }

// NewOAuthTurner wraps a Claude subscription client, keeping its bearer token
// live across the life of the session that holds it.
func NewOAuthTurner(inner engine.Turner, src TokenSource) *Turner {
	return &Turner{inner: inner, src: &src}
}

func (t *Turner) TurnCtx(ctx context.Context, opts gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	return t.run(ctx, opts, func(o gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
		return t.inner.TurnCtx(ctx, o)
	})
}

func (t *Turner) TurnStreamCtx(ctx context.Context, opts gollama.RequestOptions, onDelta func(string)) (*gollama.ResponseMessageGenerate, error) {
	stream, ok := t.inner.(engine.StreamTurner)
	if !ok {
		return t.TurnCtx(ctx, opts)
	}
	return t.run(ctx, opts, func(o gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
		return stream.TurnStreamCtx(ctx, o, onDelta)
	})
}

// run installs a live credential (when this Turner has a token source), calls
// the wrapped turn, and retries it exactly once against a forcibly refreshed
// token if the provider rejected the credential as no longer valid. A retry is
// safe because a rejected request never reached inference.
//
// Credential-carrying turns are serialized: installing a token mutates the
// transport's header map, which a request in flight on the same client reads.
// A client is built per loop and its turns are already sequential, so this only
// makes an unsupported sharing pattern slow instead of racy.
func (t *Turner) run(ctx context.Context, opts gollama.RequestOptions, do func(gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error)) (*gollama.ResponseMessageGenerate, error) {
	opts = PrefixSystem(opts)
	if t.src == nil {
		return do(opts)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	tokenCtx, cancel := context.WithTimeout(ctx, tokenTimeout)
	tok, err := t.src.Token(tokenCtx)
	cancel()
	if err != nil {
		return nil, err
	}
	t.install(tok)
	resp, err := do(opts)
	if err == nil || !IsRevokedCredential(err) {
		return resp, err
	}
	tokenCtx, cancel = context.WithTimeout(ctx, tokenTimeout)
	fresh, refreshErr := t.src.Refresh(tokenCtx, tok)
	cancel()
	// A failed recovery is reported as the provider's original rejection: it is
	// the actionable error, and the refresh failure is its consequence.
	if refreshErr != nil || fresh == tok {
		return resp, err
	}
	t.install(fresh)
	return do(opts)
}

// install sets the transport's bearer credential, skipping the write when the
// token is unchanged (the common case). Callers hold t.mu.
func (t *Turner) install(token string) {
	if token == t.using {
		return
	}
	t.src.Apply(token)
	t.using = token
}

// tokenTimeout bounds a token-endpoint round trip taken on the turn's path.
const tokenTimeout = 30 * time.Second

// IsRevokedCredential reports whether err is the provider rejecting the OAuth
// access token itself — the shape a token-family rotation elsewhere produces
// ("OAuth access token has been revoked", HTTP 401) — rather than a request the
// credential was merely not entitled to make. Matching is textual because the
// error crosses the gollama transport as a formatted status + body string.
func IsRevokedCredential(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "401") || !strings.Contains(msg, "authentication_error") {
		return false
	}
	return strings.Contains(msg, "revoked") || strings.Contains(msg, "expired")
}
