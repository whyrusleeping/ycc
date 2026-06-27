package server

import (
	"context"
	"crypto/subtle"
	"errors"

	"connectrpc.com/connect"
)

var errNoSession = errors.New("no such session")
var errNoPath = errors.New("project path is required")

// authInterceptor enforces "Authorization: Bearer <token>" on every RPC,
// including streaming handlers (so Subscribe is guarded too). An empty token
// disables auth, intended only for loopback development.
type authInterceptor struct{ token string }

// NewAuthInterceptor returns a bearer-token Connect interceptor.
func NewAuthInterceptor(token string) connect.Interceptor { return authInterceptor{token: token} }

func (a authInterceptor) ok(auth string) bool {
	if a.token == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(auth), []byte("Bearer "+a.token)) == 1
}

func (a authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if !a.ok(req.Header().Get("Authorization")) {
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or missing bearer token"))
		}
		return next(ctx, req)
	}
}

func (a authInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (a authInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if !a.ok(conn.RequestHeader().Get("Authorization")) {
			return connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or missing bearer token"))
		}
		return next(ctx, conn)
	}
}
