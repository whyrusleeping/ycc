package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/whyrusleeping/ycc/internal/googleauth"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

const googleLoginLifetime = 10 * time.Minute

// googleLoginState mirrors openaiLoginState: one memory-only pending attempt,
// superseded by a new begin but never racing an exchange in flight.
type googleLoginState struct {
	mu         sync.Mutex
	pending    *googleLoginAttempt
	exchanging bool
}

type googleLoginAttempt struct {
	id      string
	state   string
	pkce    googleauth.PKCE
	expires time.Time
}

func (s *Server) BeginGoogleLogin(ctx context.Context, _ *connect.Request[v1.BeginGoogleLoginRequest]) (*connect.Response[v1.BeginGoogleLoginResponse], error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state := &s.googleLogin
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.exchanging {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Google login is being completed; wait for it to finish"))
	}
	pkce, err := googleauth.NewPKCE()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not create Google login; try again"))
	}
	var raw [48]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not create Google login; try again"))
	}
	attempt := &googleLoginAttempt{
		id:      base64.RawURLEncoding.EncodeToString(raw[:32]),
		state:   base64.RawURLEncoding.EncodeToString(raw[32:]),
		pkce:    pkce,
		expires: time.Now().Add(googleLoginLifetime),
	}
	state.pending = attempt
	res := connect.NewResponse(&v1.BeginGoogleLoginResponse{
		AttemptId: attempt.id, AuthorizationUrl: googleauth.AuthorizeURL(pkce, attempt.state),
		ExpiresAtUnix: attempt.expires.Unix(),
	})
	res.Header().Set("Cache-Control", "no-store")
	return res, nil
}

func (s *Server) CompleteGoogleLogin(ctx context.Context, req *connect.Request[v1.CompleteGoogleLoginRequest]) (*connect.Response[v1.CompleteGoogleLoginResponse], error) {
	state := &s.googleLogin
	state.mu.Lock()
	attempt := state.pending
	if attempt == nil || !time.Now().Before(attempt.expires) || attempt.id != req.Msg.AttemptId {
		if attempt != nil && !time.Now().Before(attempt.expires) {
			state.pending = nil
		}
		state.mu.Unlock()
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Google login expired, was replaced, or was already used; start a new login"))
	}
	// Even invalid input consumes the attempt; a code is never replayed.
	state.pending = nil
	state.exchanging = true
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		state.exchanging = false
		state.mu.Unlock()
	}()

	// A bare code is accepted: the attempt's PKCE verifier binds it, so a code
	// from another attempt fails the exchange. A pasted URL must carry this
	// attempt's state.
	code, err := googleauth.ParsePasted(strings.TrimSpace(req.Msg.Code), attempt.state)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("paste the code Google's page shows, or that page's full URL, from this login; start a new login to try again"))
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	creds, err := googleauth.Exchange(ctx, code, attempt.pkce)
	// Provider responses can echo codes/tokens. Never wrap or log the raw error.
	if err != nil || creds == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Google login exchange failed or timed out; start a new login and use its new code"))
	}
	if err := googleauth.Save(creds); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not save Google credentials on the daemon; check its secrets-store permissions and disk space, then start a new login"))
	}
	res := connect.NewResponse(&v1.CompleteGoogleLoginResponse{Email: creds.Email})
	if s.mgr == nil {
		return res, nil
	}
	if adopted, err := s.mgr.AdoptGoogleLogin(); err != nil {
		log.Printf("google login: saved, but could not configure the fallback model: %v", err)
	} else if adopted {
		log.Printf("google login: model %q now uses the Google subscription", "gemini")
	}
	return res, nil
}

func (s *Server) CancelGoogleLogin(_ context.Context, req *connect.Request[v1.CancelGoogleLoginRequest]) (*connect.Response[v1.CancelGoogleLoginResponse], error) {
	state := &s.googleLogin
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pending != nil && state.pending.id == req.Msg.AttemptId {
		state.pending = nil
	}
	return connect.NewResponse(&v1.CancelGoogleLoginResponse{}), nil
}
