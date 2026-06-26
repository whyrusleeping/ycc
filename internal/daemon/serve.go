// Package daemon hosts the ycc service (session manager + Connect-RPC) and the
// client-side helpers to reach it. Persistence is opt-in: a one-shot in-process
// daemon (StartInProcess) is tied to the client's lifetime, while `ycc daemon`
// (Serve) and `ycc --background` (EnsureBackgroundDaemon) run a persistent one.
package daemon

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/server"
	"github.com/whyrusleeping/ycc/internal/session"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

// Options configures the daemon.
type Options struct {
	Addr       string
	Workspace  string
	ConfigPath string // TOML config; empty => single Anthropic backend from Model/BaseURL/KeyEnv
	Model      string
	BaseURL    string
	KeyEnv     string
	MaxTokens  int
	Token      string
	TLSCert    string
	TLSKey     string
}

// buildHandler constructs the session manager and Connect HTTP handler from
// Options. It is shared by the foreground/persistent Serve path and the
// one-shot in-process path so both expose an identical tool surface.
func buildHandler(o Options) (http.Handler, error) {
	var cfg *config.Config
	if o.ConfigPath != "" {
		c, err := config.Load(o.ConfigPath)
		if err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
		cfg = c
		log.Printf("loaded config %s: coordinator=%s implementer=%s reviewers=%v",
			o.ConfigPath, cfg.Roles.Coordinator, cfg.Roles.Implementer, cfg.Roles.Reviewers)
	} else {
		cfg = config.DefaultAnthropic(o.BaseURL, o.Model, o.KeyEnv, o.MaxTokens)
		log.Printf("using single Anthropic backend (model=%s)", o.Model)
	}

	mgr := session.NewManager(config.NewRegistry(cfg), o.Workspace)
	srv := server.New(mgr)

	mux := http.NewServeMux()
	path, handler := yccv1connect.NewSessionServiceHandler(
		srv,
		connect.WithInterceptors(server.NewAuthInterceptor(o.Token)),
	)
	mux.Handle(path, handler)
	return h2c.NewHandler(mux, &http2.Server{}), nil
}

// Serve builds the registry, session manager, and Connect handler and serves
// until the process exits. It blocks. Used by the explicit, persistent
// `ycc daemon`.
func Serve(o Options) error {
	handler, err := buildHandler(o)
	if err != nil {
		return err
	}

	usingTLS := o.TLSCert != "" && o.TLSKey != ""
	if !isLoopback(o.Addr) {
		if o.Token == "" {
			return fmt.Errorf("refusing to bind non-loopback address %s without a token", o.Addr)
		}
		if !usingTLS {
			log.Printf("warning: binding non-loopback address %s without TLS; traffic is cleartext", o.Addr)
		}
	}

	httpSrv := &http.Server{Addr: o.Addr, Handler: handler}
	log.Printf("ycc daemon listening on %s (workspace=%s tls=%v)", o.Addr, o.Workspace, usingTLS)
	if usingTLS {
		return httpSrv.ListenAndServeTLS(o.TLSCert, o.TLSKey)
	}
	return httpSrv.ListenAndServe()
}

// InProcess is a running one-shot daemon: a server bound to an ephemeral
// loopback address, tied to the caller's lifetime. Call Shutdown (or Close) to
// tear it down — the listener and any in-flight work end with it.
type InProcess struct {
	Addr    string // base URL, e.g. "http://127.0.0.1:54321"
	httpSrv *http.Server
}

// StartInProcess starts the daemon in-process on an ephemeral loopback address
// (127.0.0.1:0) and returns once it is listening. There is no persistence:
// closing it ends in-flight agent work. The returned InProcess must be shut
// down by the caller (defer Shutdown / Close) so no listener survives exit.
func StartInProcess(o Options) (*InProcess, error) {
	o.Addr = "127.0.0.1:0"
	handler, err := buildHandler(o)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", o.Addr)
	if err != nil {
		return nil, fmt.Errorf("listen ephemeral: %w", err)
	}
	httpSrv := &http.Server{Handler: handler}
	ip := &InProcess{
		Addr:    "http://" + ln.Addr().String(),
		httpSrv: httpSrv,
	}
	go func() {
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("in-process daemon: %v", err)
		}
	}()
	return ip, nil
}

// Shutdown gracefully stops the in-process daemon, ending in-flight work.
func (p *InProcess) Shutdown() error {
	if p == nil || p.httpSrv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return p.httpSrv.Shutdown(ctx)
}

// Close forcibly stops the in-process daemon. Safe to call multiple times.
func (p *InProcess) Close() error {
	if p == nil || p.httpSrv == nil {
		return nil
	}
	return p.httpSrv.Close()
}

func isLoopback(addr string) bool {
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
	}
	return host == "" || host == "127.0.0.1" || host == "localhost" || host == "::1" || host == "[::1]"
}
