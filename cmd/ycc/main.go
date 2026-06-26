// Command ycc is the thin client for the yccd daemon (M1). It can start a
// session and stream its events, attach to an existing session (optionally
// replaying from a seq offset), list sessions, and send follow-up input typed on
// stdin. The rich TUI is a later milestone; this proves the client/server seam.
//
//	ycc start "add a hello.txt that says hi"      # start + stream; type to prod
//	ycc attach s_abc123 --from 0                  # re-attach, replay from start
//	ycc list
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

func main() {
	// Global flags precede the subcommand; subcommand-specific flags follow it.
	global := flag.NewFlagSet("ycc", flag.ExitOnError)
	addr := global.String("addr", "http://127.0.0.1:8787", "daemon base URL (http:// uses cleartext h2c)")
	token := global.String("token", os.Getenv("YCC_TOKEN"), "bearer token")
	global.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: ycc [-addr URL] [-token T] <start|attach|list> [args]")
		global.PrintDefaults()
	}
	global.Parse(os.Args[1:])

	args := global.Args()
	if len(args) == 0 {
		global.Usage()
		os.Exit(2)
	}

	client := yccv1connect.NewSessionServiceClient(httpClient(*addr), *addr, connect.WithInterceptors(bearer(*token)))
	ctx := context.Background()

	switch args[0] {
	case "start":
		// Positional (task) comes first; flags follow it, so parse args after it.
		if len(args) < 2 {
			fatal("usage: ycc start \"<task>\" [--workspace DIR]")
		}
		task := args[1]
		fs := flag.NewFlagSet("start", flag.ExitOnError)
		workspace := fs.String("workspace", "", "workspace dir (default: daemon's default)")
		fs.Parse(args[2:])
		resp, err := client.StartSession(ctx, connect.NewRequest(&v1.StartSessionRequest{
			Workspace: *workspace,
			Prompt:    task,
		}))
		if err != nil {
			fatal("StartSession: %v", err)
		}
		id := resp.Msg.SessionId
		fmt.Printf("session %s\n", id)
		go readStdinInto(ctx, client, id)
		stream(ctx, client, id, 0)

	case "attach":
		// Positional (session id) comes first; flags follow it.
		if len(args) < 2 {
			fatal("usage: ycc attach <session-id> [--from N]")
		}
		id := args[1]
		fs := flag.NewFlagSet("attach", flag.ExitOnError)
		fromSeq := fs.Int64("from", 0, "replay events with seq greater than this")
		fs.Parse(args[2:])
		go readStdinInto(ctx, client, id)
		stream(ctx, client, id, *fromSeq)

	case "list":
		resp, err := client.ListSessions(ctx, connect.NewRequest(&v1.ListSessionsRequest{}))
		if err != nil {
			fatal("ListSessions: %v", err)
		}
		if len(resp.Msg.Sessions) == 0 {
			fmt.Println("(no sessions)")
			return
		}
		for _, s := range resp.Msg.Sessions {
			fmt.Printf("%s  %-8s %-8s %s\n", s.SessionId, s.Mode, s.Status, s.Workspace)
		}

	default:
		fatal("unknown command %q", args[0])
	}
}

func stream(ctx context.Context, client yccv1connect.SessionServiceClient, id string, from int64) {
	s, err := client.Subscribe(ctx, connect.NewRequest(&v1.SubscribeRequest{SessionId: id, FromSeq: from}))
	if err != nil {
		fatal("Subscribe: %v", err)
	}
	for s.Receive() {
		printEvent(s.Msg())
	}
	if err := s.Err(); err != nil {
		fatal("stream: %v", err)
	}
}

func readStdinInto(ctx context.Context, client yccv1connect.SessionServiceClient, id string) {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if _, err := client.SendInput(ctx, connect.NewRequest(&v1.SendInputRequest{SessionId: id, Text: line})); err != nil {
			fmt.Fprintf(os.Stderr, "send: %v\n", err)
		}
	}
}

func printEvent(ev *v1.Event) {
	var data map[string]any
	if ev.DataJson != "" {
		json.Unmarshal([]byte(ev.DataJson), &data)
	}
	get := func(k string) string {
		if s, ok := data[k].(string); ok {
			return s
		}
		return ""
	}
	line := fmt.Sprintf("[%3d] %-12s %-14s", ev.Seq, ev.Actor, ev.Type)
	switch ev.Type {
	case "tool_call":
		line += fmt.Sprintf(" %s(%s)", get("name"), truncate(get("args"), 100))
	case "tool_result":
		line += " " + truncate(get("result"), 120)
	case "model_turn":
		line += " " + truncate(get("text"), 200)
	case "user_input":
		line += " > " + truncate(get("text"), 200)
	case "session_idle":
		line += " " + truncate(get("report"), 200)
	default:
		if m := get("msg"); m != "" {
			line += " " + m
		}
	}
	fmt.Println(line)
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// httpClient returns an HTTP client suitable for the daemon URL: an h2c
// (cleartext HTTP/2) transport for http:// URLs, or the default TLS-capable
// client for https://.
func httpClient(addr string) *http.Client {
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

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
