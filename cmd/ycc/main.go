// Command ycc is the single ycc entrypoint: client, TUI, and daemon in one
// binary. With no subcommand it launches the interactive TUI; for local use it
// auto-starts a background daemon (which persists after ycc exits), or attaches
// to a remote one with -addr.
//
//	ycc                                  # TUI home menu (auto-starts a local daemon)
//	ycc start "add a hello.txt"          # start a session, stream it; type to prod
//	ycc attach s_abc123 --from 0         # re-attach, replay from a seq offset
//	ycc list | ycc modes
//	ycc -addr https://host:8787 -token T # attach to a remote daemon
//	ycc daemon -addr 0.0.0.0:8787 -token T -tls-cert c.pem -tls-key k.pem
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/daemon"
	"github.com/whyrusleeping/ycc/internal/tui"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

func main() {
	// The daemon subcommand runs the service itself (used by `ycc daemon` and by
	// the auto-start path); it has its own flags.
	if len(os.Args) > 1 && os.Args[1] == "daemon" {
		runDaemon(os.Args[2:])
		return
	}

	// Global flags precede the subcommand; subcommand-specific flags follow it.
	global := flag.NewFlagSet("ycc", flag.ExitOnError)
	addr := global.String("addr", "", "remote daemon URL to attach to (default: auto-managed local daemon)")
	token := global.String("token", os.Getenv("YCC_TOKEN"), "bearer token (for -addr)")
	workspace := global.String("workspace", "", "workspace for new sessions (default: current directory)")
	configPath := global.String("config", "", "TOML model config for the auto-started local daemon")
	global.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: ycc [-addr URL] [-token T] [<start|attach|list|modes|daemon>] [args]")
		fmt.Fprintln(os.Stderr, "  with no subcommand, launches the interactive TUI (home menu)")
		global.PrintDefaults()
	}
	global.Parse(os.Args[1:])

	// Sessions bind to the current directory unless overridden, so one daemon can
	// serve many workspaces.
	ws := *workspace
	if ws == "" {
		if cwd, err := os.Getwd(); err == nil {
			ws = cwd
		}
	}

	// Resolve the daemon to talk to: a remote via -addr, or an auto-managed local one.
	target, tok := *addr, *token
	if target == "" {
		if err := daemon.EnsureLocalDaemon(ws, *configPath); err != nil {
			fatal("%v", err)
		}
		target, tok = daemon.LocalAddr, ""
	}

	client := daemon.DialClient(target, tok)
	ctx := context.Background()

	args := global.Args()
	if len(args) == 0 {
		if err := tui.Run(ctx, client, ws); err != nil {
			fatal("tui: %v", err)
		}
		return
	}

	switch args[0] {
	case "start":
		// The task is an optional leading positional (omit it for work mode to let
		// the coordinator pick from the backlog); flags may follow it.
		task := ""
		rest := args[1:]
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			task, rest = rest[0], rest[1:]
		}
		fs := flag.NewFlagSet("start", flag.ExitOnError)
		workspace := fs.String("workspace", ws, "workspace dir (default: current directory)")
		mode := fs.String("mode", "", "session mode (default: work)")
		level := fs.String("level", "", "interaction level: interactive | judgement | autonomous")
		fs.Parse(rest)
		resp, err := client.StartSession(ctx, connect.NewRequest(&v1.StartSessionRequest{
			Workspace:        *workspace,
			Mode:             *mode,
			InteractionLevel: *level,
			Prompt:           task,
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

	case "modes":
		resp, err := client.ListModes(ctx, connect.NewRequest(&v1.ListModesRequest{}))
		if err != nil {
			fatal("ListModes: %v", err)
		}
		for _, mode := range resp.Msg.Modes {
			fmt.Printf("%-9s %s\n", mode.Name, mode.Description)
		}

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

// runDaemon parses daemon flags and serves until killed. Used by `ycc daemon`
// and by the local auto-start path.
func runDaemon(argv []string) {
	fs := flag.NewFlagSet("ycc daemon", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8787", "address to listen on")
	workspace := fs.String("workspace", ".", "default workspace for sessions that don't specify one")
	configPath := fs.String("config", "", "TOML config file (models + roles)")
	model := fs.String("model", "claude-opus-4-8", "fallback model id (when no -config)")
	baseURL := fs.String("base-url", "https://api.anthropic.com", "fallback API base URL (when no -config)")
	keyEnv := fs.String("key-env", "ANTHROPIC_API_KEY", "fallback API key env var (when no -config)")
	maxTok := fs.Int("max-tokens", 8192, "fallback max tokens per turn (when no -config)")
	token := fs.String("token", os.Getenv("YCC_TOKEN"), "bearer token clients must present (empty disables auth)")
	tlsCert := fs.String("tls-cert", "", "TLS certificate file (enables HTTPS)")
	tlsKey := fs.String("tls-key", "", "TLS key file")
	fs.Parse(argv)

	err := daemon.Serve(daemon.Options{
		Addr: *addr, Workspace: *workspace, ConfigPath: *configPath,
		Model: *model, BaseURL: *baseURL, KeyEnv: *keyEnv, MaxTokens: *maxTok,
		Token: *token, TLSCert: *tlsCert, TLSKey: *tlsKey,
	})
	if err != nil {
		fatal("daemon: %v", err)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
