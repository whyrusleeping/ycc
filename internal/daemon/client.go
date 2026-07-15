package daemon

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"

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

// EnsureBackgroundDaemon makes sure a persistent local daemon is running on
// LocalAddr, spawning a detached `ycc daemon` (which survives this process) if
// none is reachable. It returns once the daemon answers. This is the opt-in
// persistence path used by `ycc --background`; configPath is auto-discovered
// when empty. token is the bearer token to probe with (and is passed to a
// newly spawned daemon so remote clients keep working): an already-running
// token-protected daemon rejects an empty-token probe, and spawning a second
// daemon on the same port would just fail to bind.
func EnsureBackgroundDaemon(workspace, configPath, token string) error {
	if Reachable(LocalAddr, token) {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate ycc binary: %w", err)
	}
	if configPath == "" {
		configPath = DiscoverConfig(workspace)
	}
	// The spawned daemon inherits this process's environment (including the API
	// key). Warn if it looks like the agent won't be able to reach a model — the
	// daemon persists, so a keyless start would 401 every later session.
	if configPath == "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "ycc: warning: ANTHROPIC_API_KEY is unset and no ycc.toml found; the daemon won't be able to reach a model")
	}

	host := strings.TrimPrefix(LocalAddr, "http://")
	args := []string{"daemon", "-addr", host}
	if workspace != "" {
		args = append(args, "-workspace", workspace)
	}
	if configPath != "" {
		args = append(args, "-config", configPath)
	}
	if token != "" {
		args = append(args, "-token", token)
	}

	cmd := exec.Command(self, args...)
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach: survive ycc exit
	// A detached daemon must NOT inherit our std{in,out,err}. If we were spawned
	// from inside a shell pipeline (e.g. an agent running `ycc ... | tail`), an
	// inherited pipe's write end stays open for the daemon's whole life, so the
	// reader never sees EOF and the pipeline wedges forever. Redirect stdin from
	// /dev/null and stdout/stderr to the daemon log, falling back to /dev/null —
	// never leaving them nil (which would inherit ours).
	devnull, _ := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if devnull != nil {
		defer devnull.Close()
		cmd.Stdin = devnull
		cmd.Stdout, cmd.Stderr = devnull, devnull
	}
	logPath := daemonLogPath()
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		defer f.Close()
		cmd.Stdout, cmd.Stderr = f, f
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start local daemon: %w", err)
	}
	_ = cmd.Process.Release()

	for i := 0; i < 60; i++ {
		if Reachable(LocalAddr, token) {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("local daemon did not become ready; see %s", logPath)
}

// DiscoverConfig looks for a ycc.toml in the workspace, then the user config dir.
func DiscoverConfig(workspace string) string {
	candidates := []string{}
	if workspace != "" {
		candidates = append(candidates, filepath.Join(workspace, "ycc.toml"))
	}
	if dir, err := os.UserConfigDir(); err == nil {
		candidates = append(candidates, filepath.Join(dir, "ycc", "ycc.toml"))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func daemonLogPath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "ycc-daemon.log")
	}
	d := filepath.Join(dir, "ycc")
	os.MkdirAll(d, 0o755)
	return filepath.Join(d, "daemon.log")
}

func httpClientFor(addr string) *http.Client {
	if strings.HasPrefix(addr, "https://") {
		return http.DefaultClient
	}
	return &http.Client{
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
