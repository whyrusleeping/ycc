// Command yccd is the ycc daemon (spec §3). It owns sessions, runs agent loops
// against the workspace filesystem, persists per-session event logs, and exposes
// the Connect-RPC SessionService. The TUI/CLI clients are thin and talk to it.
//
// Loopback development (cleartext HTTP/2 via h2c, no auth):
//
//	ANTHROPIC_API_KEY=… yccd -workspace .
//
// Remote (bind a real interface): set -token and TLS:
//
//	yccd -addr 0.0.0.0:8787 -token "$YCC_TOKEN" -tls-cert cert.pem -tls-key key.pem
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"strings"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/server"
	"github.com/whyrusleeping/ycc/internal/session"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8787", "address to listen on")
	workspace := flag.String("workspace", ".", "default workspace directory for new sessions")
	model := flag.String("model", "claude-opus-4-8", "model id")
	baseURL := flag.String("base-url", "https://api.anthropic.com", "API base URL")
	keyEnv := flag.String("key-env", "ANTHROPIC_API_KEY", "env var holding the API key")
	bearer := flag.Bool("bearer", false, "send the key as a Bearer token (OpenAI-compatible) instead of x-api-key")
	maxTok := flag.Int("max-tokens", 8192, "max tokens per turn")
	token := flag.String("token", os.Getenv("YCC_TOKEN"), "bearer token clients must present (empty disables auth)")
	tlsCert := flag.String("tls-cert", "", "TLS certificate file (enables HTTPS)")
	tlsKey := flag.String("tls-key", "", "TLS key file")
	flag.Parse()

	key := os.Getenv(*keyEnv)
	if key == "" {
		log.Printf("warning: %s is not set; the agent cannot reach the backend", *keyEnv)
	}
	newClient := func() engine.Turner {
		c := gollama.NewClient(*baseURL)
		if key != "" {
			if *bearer {
				c.SetBearerToken(key)
			} else {
				c.SetAPIKey(key)
			}
		}
		return c
	}

	mgr := session.NewManager(newClient, *model, *maxTok, *workspace)
	srv := server.New(mgr)

	mux := http.NewServeMux()
	path, handler := yccv1connect.NewSessionServiceHandler(
		srv,
		connect.WithInterceptors(server.NewAuthInterceptor(*token)),
	)
	mux.Handle(path, handler)

	usingTLS := *tlsCert != "" && *tlsKey != ""
	if !isLoopback(*addr) {
		if *token == "" {
			log.Fatalf("refusing to bind non-loopback address %s without -token", *addr)
		}
		if !usingTLS {
			log.Printf("warning: binding non-loopback address %s without TLS; traffic is cleartext", *addr)
		}
	}
	if *token == "" {
		log.Printf("auth disabled (no -token); intended for loopback only")
	}

	httpSrv := &http.Server{
		Addr:    *addr,
		Handler: h2c.NewHandler(mux, &http2.Server{}),
	}

	log.Printf("yccd listening on %s (workspace=%s model=%s tls=%v)", *addr, *workspace, *model, usingTLS)
	var err error
	if usingTLS {
		err = httpSrv.ListenAndServeTLS(*tlsCert, *tlsKey)
	} else {
		err = httpSrv.ListenAndServe()
	}
	if err != nil {
		log.Fatal(err)
	}
}

func isLoopback(addr string) bool {
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
	}
	return host == "" || host == "127.0.0.1" || host == "localhost" || host == "::1" || host == "[::1]"
}
