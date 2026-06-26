// Package daemon hosts the ycc service (session manager + Connect-RPC) and the
// client-side helpers to reach it — including auto-starting a local daemon so a
// single `ycc` binary "just works" while still allowing attachment to a remote.
package daemon

import (
	"fmt"
	"log"
	"net/http"
	"strings"

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

// Serve builds the registry, session manager, and Connect handler and serves
// until the process exits. It blocks.
func Serve(o Options) error {
	var cfg *config.Config
	if o.ConfigPath != "" {
		c, err := config.Load(o.ConfigPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
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

	usingTLS := o.TLSCert != "" && o.TLSKey != ""
	if !isLoopback(o.Addr) {
		if o.Token == "" {
			return fmt.Errorf("refusing to bind non-loopback address %s without a token", o.Addr)
		}
		if !usingTLS {
			log.Printf("warning: binding non-loopback address %s without TLS; traffic is cleartext", o.Addr)
		}
	}

	httpSrv := &http.Server{Addr: o.Addr, Handler: h2c.NewHandler(mux, &http2.Server{})}
	log.Printf("ycc daemon listening on %s (workspace=%s tls=%v)", o.Addr, o.Workspace, usingTLS)
	if usingTLS {
		return httpSrv.ListenAndServeTLS(o.TLSCert, o.TLSKey)
	}
	return httpSrv.ListenAndServe()
}

func isLoopback(addr string) bool {
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
	}
	return host == "" || host == "127.0.0.1" || host == "localhost" || host == "::1" || host == "[::1]"
}
