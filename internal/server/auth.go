package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"

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

// RequireBearer checks auth before Connect reads or decompresses request bodies.
// The interceptor remains a second layer after decoding. An empty token disables auth.
func RequireBearer(token string, next http.Handler) http.Handler {
	auth := authInterceptor{token: token}
	ew := connect.NewErrorWriter()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.ok(r.Header.Get("Authorization")) {
			// ErrorWriter does not read the body; unknown protocols also get
			// Connect-shaped JSON, while streaming/gRPC get their wire format.
			_ = ew.Write(w, r, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or missing bearer token")))
			return
		}
		next.ServeHTTP(w, r)
	})
}

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
