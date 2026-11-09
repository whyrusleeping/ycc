package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/whyrusleeping/ycc/internal/openaiauth"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

const openaiLoginLifetime = 10 * time.Minute

// This state is independent of the daemon's Anthropic login. A new begin
// supersedes a pending attempt, but cannot race an exchange/store in flight.
// Attempts are memory-only; all callers share the daemon bearer-token boundary.
type openaiLoginState struct {
	mu         sync.Mutex
	pending    *openaiLoginAttempt
	exchanging bool
}

type openaiLoginAttempt struct {
	id      string
	state   string
	pkce    openaiauth.PKCE
	expires time.Time
}

func openaiLoginRedirectURI() string {
	return fmt.Sprintf("http://localhost:%d/auth/callback", openaiauth.CallbackPort)
}

func (s *Server) BeginOpenAILogin(ctx context.Context, _ *connect.Request[v1.BeginOpenAILoginRequest]) (*connect.Response[v1.BeginOpenAILoginResponse], error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state := &s.openaiLogin
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.exchanging {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("ChatGPT login is being completed; wait for it to finish"))
	}
	pkce, err := openaiauth.NewPKCE()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not create ChatGPT login; try again"))
	}
	var raw [64]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not create ChatGPT login; try again"))
	}
	attempt := &openaiLoginAttempt{
		id:      base64.RawURLEncoding.EncodeToString(raw[:32]),
		state:   base64.RawURLEncoding.EncodeToString(raw[32:]),
		pkce:    pkce,
		expires: time.Now().Add(openaiLoginLifetime),
	}
	state.pending = attempt
	res := connect.NewResponse(&v1.BeginOpenAILoginResponse{
		AttemptId: attempt.id, AuthorizationUrl: openaiauth.AuthorizeURL(pkce, attempt.state, openaiLoginRedirectURI()),
		ExpiresAtUnix: attempt.expires.Unix(),
	})
	res.Header().Set("Cache-Control", "no-store")
	return res, nil
}

func (s *Server) CompleteOpenAILogin(ctx context.Context, req *connect.Request[v1.CompleteOpenAILoginRequest]) (*connect.Response[v1.CompleteOpenAILoginResponse], error) {
	state := &s.openaiLogin
	state.mu.Lock()
	attempt := state.pending
	if attempt == nil || !time.Now().Before(attempt.expires) || attempt.id != req.Msg.AttemptId {
		if attempt != nil && !time.Now().Before(attempt.expires) {
			state.pending = nil
		}
		state.mu.Unlock()
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("ChatGPT login expired, was replaced, or was already used; start a new login"))
	}
	// Even invalid input consumes the matching attempt. Never replay a code
	// after a timeout or ambiguous response from the provider.
	state.pending = nil
	state.exchanging = true
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		state.exchanging = false
		state.mu.Unlock()
	}()

	pasted := strings.TrimSpace(req.Msg.Callback)
	// The CLI accepts a bare code or a callback without state. Remote clients
	// must supply state so the pasted code is bound to this daemon attempt.
	query := pasted
	if i := strings.IndexByte(query, '?'); i >= 0 {
		query = query[i+1:]
	}
	var code string
	if len(pasted) <= 8192 {
		q, err := url.ParseQuery(query)
		if err == nil && len(q["state"]) == 1 && len(q["code"]) == 1 &&
			subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(attempt.state)) == 1 {
			code, _ = openaiauth.ParsePastedCallback(pasted, attempt.state)
		}
	}
	if code == "" || strings.ContainsAny(code, " \t\r\n") {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("paste the full redirect URL or query from this ChatGPT login, including code and state; start a new login to try again"))
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	creds, err := openaiauth.Exchange(ctx, code, attempt.pkce, openaiLoginRedirectURI())
	// Provider responses can echo codes/tokens. Never wrap or log the raw error.
	if err != nil || creds == nil || creds.RefreshToken == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("ChatGPT login exchange failed or timed out; start a new login and use its new redirect URL"))
	}
	if ctx.Err() != nil {
		return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("ChatGPT login timed out; start a new login"))
	}
	if err := openaiauth.Save(creds); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not save ChatGPT credentials on the daemon; check its secrets-store permissions and disk space, then start a new login"))
	}
	if s.mgr == nil {
		return connect.NewResponse(&v1.CompleteOpenAILoginResponse{}), nil
	}
	if adopted, err := s.mgr.AdoptOpenAILogin(); err != nil {
		log.Printf("openai login: saved, but could not configure the fallback model: %v", err)
	} else if adopted {
		log.Printf("openai login: model %q now uses the ChatGPT subscription", "chatgpt")
	}
	return connect.NewResponse(&v1.CompleteOpenAILoginResponse{}), nil
}

func (s *Server) CancelOpenAILogin(_ context.Context, req *connect.Request[v1.CancelOpenAILoginRequest]) (*connect.Response[v1.CancelOpenAILoginResponse], error) {
	state := &s.openaiLogin
	state.mu.Lock()
	defer state.mu.Unlock()
	// A stale screen cannot cancel another client's newer attempt. Submitted
	// exchanges may complete; cancel never removes stored credentials.
	if state.pending != nil && state.pending.id == req.Msg.AttemptId {
		state.pending = nil
	}
	return connect.NewResponse(&v1.CancelOpenAILoginResponse{}), nil
}
