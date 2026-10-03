package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/whyrusleeping/ycc/internal/anthropicauth"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

const anthropicLoginLifetime = 10 * time.Minute

// Only one pending login is needed for the daemon's shared Anthropic account.
// Attempts are memory-only, bounded, and consumed before any exchange. A new
// begin supersedes a pending attempt, but cannot race an exchange/store in flight.
// All callers share the daemon bearer-token trust boundary.
type anthropicLoginState struct {
	mu         sync.Mutex
	pending    *anthropicLoginAttempt
	exchanging bool
}

type anthropicLoginAttempt struct {
	id      string
	pkce    anthropicauth.PKCE
	expires time.Time
}

func (s *Server) BeginAnthropicLogin(ctx context.Context, _ *connect.Request[v1.BeginAnthropicLoginRequest]) (*connect.Response[v1.BeginAnthropicLoginResponse], error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state := &s.anthropicLogin
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.exchanging {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Anthropic login is being completed; wait for it to finish"))
	}
	pkce, err := anthropicauth.NewPKCE()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not create Anthropic login; try again"))
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not create Anthropic login; try again"))
	}
	attempt := &anthropicLoginAttempt{
		id: base64.RawURLEncoding.EncodeToString(raw[:]), pkce: pkce,
		expires: time.Now().Add(anthropicLoginLifetime),
	}
	state.pending = attempt
	res := connect.NewResponse(&v1.BeginAnthropicLoginResponse{
		AttemptId: attempt.id, AuthorizationUrl: anthropicauth.AuthorizeURL(pkce),
		ExpiresAtUnix: attempt.expires.Unix(),
	})
	res.Header().Set("Cache-Control", "no-store")
	return res, nil
}

func (s *Server) CompleteAnthropicLogin(ctx context.Context, req *connect.Request[v1.CompleteAnthropicLoginRequest]) (*connect.Response[v1.CompleteAnthropicLoginResponse], error) {
	state := &s.anthropicLogin
	state.mu.Lock()
	attempt := state.pending
	if attempt == nil || !time.Now().Before(attempt.expires) || attempt.id != req.Msg.AttemptId {
		if attempt != nil && !time.Now().Before(attempt.expires) {
			state.pending = nil
		}
		state.mu.Unlock()
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Anthropic login expired, was replaced, or was already used; start a new login"))
	}
	// Even an invalid code consumes the matching attempt. Never replay a code
	// after a timeout or ambiguous response from the provider.
	state.pending = nil
	state.exchanging = true
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		state.exchanging = false
		state.mu.Unlock()
	}()

	pasted := strings.TrimSpace(req.Msg.Code)
	code, echoedState, hasState := strings.Cut(pasted, "#")
	if len(pasted) > 8192 || code == "" || !hasState || strings.ContainsAny(code, " \t\r\n") ||
		subtle.ConstantTimeCompare([]byte(echoedState), []byte(attempt.pkce.Verifier)) != 1 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("paste the full code#state from this login's Anthropic page; start a new login to try again"))
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	creds, err := anthropicauth.Exchange(ctx, pasted, attempt.pkce)
	// Provider responses can echo codes/tokens. Never wrap or log the raw error.
	if err != nil || creds == nil || creds.RefreshToken == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Anthropic login exchange failed or timed out; start a new login and use its new code"))
	}
	if ctx.Err() != nil {
		return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("Anthropic login timed out; start a new login"))
	}
	if err := anthropicauth.Save(creds); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not save Anthropic credentials on the daemon; check its secrets-store permissions and disk space, then start a new login"))
	}
	// No session, model configuration, or work-loop state is changed here.
	return connect.NewResponse(&v1.CompleteAnthropicLoginResponse{}), nil
}

func (s *Server) CancelAnthropicLogin(_ context.Context, req *connect.Request[v1.CancelAnthropicLoginRequest]) (*connect.Response[v1.CancelAnthropicLoginResponse], error) {
	state := &s.anthropicLogin
	state.mu.Lock()
	defer state.mu.Unlock()
	// A stale screen must not cancel another client's newer attempt. Exchanges
	// already submitted are single-use and may complete; cancellation only drops
	// a pending browser flow, never removes stored credentials.
	if state.pending != nil && state.pending.id == req.Msg.AttemptId {
		state.pending = nil
	}
	return connect.NewResponse(&v1.CancelAnthropicLoginResponse{}), nil
}
