package daemon

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"

	"github.com/whyrusleeping/ycc/internal/llmhttp"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

// LocalAddr is the loopback address an auto-managed local daemon listens on.
const LocalAddr = "http://127.0.0.1:8787"

// DialClient builds a SessionService client for a daemon base URL. http:// URLs
// use cleartext HTTP/2 (h2c); https:// uses TLS.
func DialClient(addr, token string) yccv1connect.SessionServiceClient {
	return yccv1connect.NewSessionServiceClient(httpClientFor(addr), addr, connect.WithInterceptors(bearer(token)))
}

// Reachable reports whether a daemon answers at addr within a short timeout.
func Reachable(addr, token string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := DialClient(addr, token).ListModes(ctx, connect.NewRequest(&v1.ListModesRequest{}))
	return err == nil
}

// UserConfigPath returns the user-level config file
// ($XDG_CONFIG_HOME/ycc/ycc.toml), the second place DiscoverConfig looks and
// where `ycc login` creates a config when none exists.
func UserConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ycc", "ycc.toml"), nil
}

// DiscoverConfig looks for a ycc.toml in the workspace, then the user config dir.
func DiscoverConfig(workspace string) string {
	candidates := []string{}
	if workspace != "" {
		candidates = append(candidates, filepath.Join(workspace, "ycc.toml"))
	}
	if p, err := UserConfigPath(); err == nil {
		candidates = append(candidates, p)
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func httpClientFor(addr string) *http.Client {
	if strings.HasPrefix(addr, "https://") {
		return &http.Client{CheckRedirect: llmhttp.CheckRedirect}
	}
	return &http.Client{
		CheckRedirect: llmhttp.CheckRedirect,
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, a string, _ *tls.Config) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, a)
			},
		},
	}
}

// bearer adds the Authorization header to unary and streaming requests.
func bearer(token string) connect.Interceptor { return bearerInterceptor{token} }

type bearerInterceptor struct{ token string }

func (b bearerInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if b.token != "" {
			req.Header().Set("Authorization", "Bearer "+b.token)
		}
		return next(ctx, req)
	}
}

func (b bearerInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		if b.token != "" {
			conn.RequestHeader().Set("Authorization", "Bearer "+b.token)
		}
		return conn
	}
}

func (b bearerInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
