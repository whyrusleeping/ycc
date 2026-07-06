// Command ycc is the single ycc entrypoint: client, TUI, and daemon in one
// binary. With no subcommand it launches the interactive TUI. Persistence is
// opt-in: plain `ycc` attaches to a persistent local daemon if one is already
// running, otherwise it starts the daemon in-process on an ephemeral loopback
// address tied to this process and torn down on exit (closing it ends in-flight
// agent work). `ycc daemon` is the explicit, persistent, foreground service;
// `ycc --background` spawns a detached persistent daemon and attaches; `--addr`
// attaches to a remote/explicit daemon.
//
//	ycc                                  # TUI home menu (one-shot in-process daemon)
//	ycc --background                     # spawn a detached persistent daemon and attach
//	ycc start "add a hello.txt"          # start a session, stream it; type to prod
//	ycc attach s_abc123 --from 0         # re-attach, replay from a seq offset
//	ycc list | ycc modes
//	ycc cost --by task --since 2026-06-01  # usage/cost breakdown by backlog task
//	ycc --addr https://host:8787 --token T # attach to a remote daemon
//	ycc daemon --addr 0.0.0.0:8787 --token T --tls-cert c.pem --tls-key k.pem
//
// The command tree is built with urfave/cli (v3), which gives every subcommand
// discoverable `--help` output and a generated command list (`ycc --help`).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"

	"connectrpc.com/connect"
	cli "github.com/urfave/cli/v3"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/daemon"
	"github.com/whyrusleeping/ycc/internal/sandbox"
	"github.com/whyrusleeping/ycc/internal/secrets"
	"github.com/whyrusleeping/ycc/internal/setup"
	"github.com/whyrusleeping/ycc/internal/tui"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

// app holds the resolved global flags shared by every subcommand. Global flags
// are captured straight into these fields via cli flag Destinations, so a
// subcommand action just reads them.
type app struct {
	addr       string // remote/explicit daemon URL (empty = local)
	token      string // bearer token for --addr
	workspace  string // workspace for new sessions (resolved to cwd if empty)
	configPath string // TOML model config for the local daemon
	background bool   // spawn a detached persistent daemon and attach
}

func main() {
	// If this process was re-executed as a sandbox helper (reviewer bash
	// confinement, see internal/sandbox), apply the policy and exec the wrapped
	// command before any CLI parsing. This never returns in the helper case.
	sandbox.MaybeHelper()

	a := &app{}
	root := newRootCommand(a)
	if err := root.Run(context.Background(), os.Args); err != nil {
		fatal("%v", err)
	}
}

// newRootCommand assembles the full command tree. Global flags are marked Local
// so they precede the subcommand (e.g. `ycc --addr X list`) and never collide
// with same-named subcommand flags such as `daemon --addr`.
func newRootCommand(a *app) *cli.Command {
	globalFlags := []cli.Flag{
		&cli.StringFlag{
			Name:        "addr",
			Usage:       "remote/explicit daemon `URL` to attach to (e.g. http://100.64.0.1:8787)",
			Destination: &a.addr,
			Local:       true,
		},
		&cli.StringFlag{
			Name:        "token",
			Usage:       "bearer `token` for --addr (or set YCC_TOKEN)",
			Sources:     cli.EnvVars("YCC_TOKEN"),
			Destination: &a.token,
			Local:       true,
		},
		&cli.StringFlag{
			Name:        "workspace",
			Usage:       "workspace `dir` for new sessions (default: current directory)",
			Destination: &a.workspace,
			Local:       true,
		},
		&cli.StringFlag{
			Name:        "config",
			Usage:       "TOML model config `file` for the local daemon",
			Destination: &a.configPath,
			Local:       true,
		},
		&cli.BoolFlag{
			Name:        "background",
			Usage:       "spawn a detached persistent daemon and attach (opt-in persistence)",
			Destination: &a.background,
			Local:       true,
		},
	}

	return &cli.Command{
		Name:  "ycc",
		Usage: "coding-agent client, TUI, and daemon in one binary",
		Description: "With no subcommand, ycc launches the interactive TUI (home menu).\n" +
			"By default the daemon runs in-process and is torn down on exit (no persistence);\n" +
			"use `ycc daemon` or `ycc --background` for a persistent daemon.",
		Flags: globalFlags,
		// Resolve the workspace once, before any subcommand action runs.
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			if a.workspace == "" {
				if cwd, err := os.Getwd(); err == nil {
					a.workspace = cwd
				}
			}
			return ctx, nil
		},
		// No subcommand: launch the TUI.
		Action: a.runTUI,
		Commands: []*cli.Command{
			a.startCommand(),
			a.attachCommand(),
			a.listCommand(),
			a.modesCommand(),
			a.stopCommand(),
			a.projectCommand(),
			a.costCommand(),
			a.specCheckCommand(),
			a.doctorCommand(),
			tokenCommand(),
			daemonCommand(),
		},
	}
}

// dial resolves the daemon to talk to, installs a signal-driven teardown for any
// one-shot in-process daemon, and returns a connected client plus a cleanup func
// the caller must defer. persistent reports whether the daemon is multi-project.
// On error no client is returned and cleanup is a no-op.
func (a *app) dial() (client yccv1connect.SessionServiceClient, persistent bool, cleanup func(), err error) {
	target, tok, persistent, shutdown, err := resolveDaemon(a.addr, a.token, a.background, a.workspace, a.configPath)
	if err != nil {
		return nil, false, func() {}, err
	}
	installSignalShutdown(shutdown)
	return daemon.DialClient(target, tok), persistent, shutdown, nil
}

// runTUI is the no-subcommand action: optionally run the first-run setup wizard,
// then attach a client and launch the interactive TUI (spec §19.1, §3.1).
func (a *app) runTUI(ctx context.Context, cmd *cli.Command) error {
	// An unrecognised subcommand lands here as a leftover positional; reject it
	// rather than silently launching the TUI.
	if cmd.Args().Present() {
		return fmt.Errorf("unknown command %q (run `ycc --help`)", cmd.Args().First())
	}
	// First-run setup wizard: when launching the TUI with no usable model
	// configuration and no fallback env key, guide the user through configuring
	// providers + roles and write ~/.config/ycc/ycc.toml, then feed that path
	// into daemon resolution.
	if a.addr == "" && !a.background && a.configPath == "" && setup.NeedsSetup(a.workspace) {
		if p, err := setup.Run(a.workspace); err == nil && p != "" {
			a.configPath = p
		}
	}
	client, persistent, cleanup, err := a.dial()
	if err != nil {
		return err
	}
	defer cleanup()
	if err := tui.Run(ctx, client, a.workspace, persistent); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	return nil
}

// startCommand starts a session and streams it; the task is an optional leading
// positional (omit it for work mode to let the coordinator pick from the
// backlog).
func (a *app) startCommand() *cli.Command {
	return &cli.Command{
		Name:      "start",
		Usage:     "start a session and stream it (type to prod the agent)",
		ArgsUsage: "[task]",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "workspace", Usage: "workspace `dir` (default: --workspace or current directory)"},
			&cli.StringFlag{Name: "project", Usage: "registered project `name` (overrides --workspace)"},
			&cli.StringFlag{Name: "mode", Usage: "session `mode` (default: work)"},
			&cli.StringFlag{Name: "level", Usage: "interaction `level`: interactive | judgement | autonomous"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			ws := cmd.String("workspace")
			if ws == "" {
				ws = a.workspace
			}
			client, _, cleanup, err := a.dial()
			if err != nil {
				return err
			}
			defer cleanup()
			resp, err := client.StartSession(ctx, connect.NewRequest(&v1.StartSessionRequest{
				Workspace:        ws,
				Project:          cmd.String("project"),
				Mode:             cmd.String("mode"),
				InteractionLevel: cmd.String("level"),
				Prompt:           cmd.Args().First(),
			}))
			if err != nil {
				return fmt.Errorf("StartSession: %w", err)
			}
			id := resp.Msg.SessionId
			fmt.Printf("session %s\n", id)
			go readStdinInto(ctx, client, id)
			return stream(ctx, client, id, 0)
		},
	}
}

// attachCommand re-attaches to a running session and replays from a seq offset.
func (a *app) attachCommand() *cli.Command {
	return &cli.Command{
		Name:      "attach",
		Usage:     "re-attach to a session and stream it",
		ArgsUsage: "<session-id>",
		Flags: []cli.Flag{
			&cli.Int64Flag{Name: "from", Usage: "replay events with seq greater than `N`"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			id := cmd.Args().First()
			if id == "" {
				return fmt.Errorf("usage: ycc attach <session-id> [--from N]")
			}
			client, _, cleanup, err := a.dial()
			if err != nil {
				return err
			}
			defer cleanup()
			go readStdinInto(ctx, client, id)
			return stream(ctx, client, id, cmd.Int64("from"))
		},
	}
}

// listCommand lists known sessions.
func (a *app) listCommand() *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "list sessions",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			client, _, cleanup, err := a.dial()
			if err != nil {
				return err
			}
			defer cleanup()
			resp, err := client.ListSessions(ctx, connect.NewRequest(&v1.ListSessionsRequest{}))
			if err != nil {
				return fmt.Errorf("ListSessions: %w", err)
			}
			if len(resp.Msg.Sessions) == 0 {
				fmt.Println("(no sessions)")
				return nil
			}
			for _, s := range resp.Msg.Sessions {
				fmt.Printf("%s  %-8s %-8s %s\n", s.SessionId, s.Mode, s.Status, s.Workspace)
			}
			return nil
		},
	}
}

// modesCommand lists the available session modes.
func (a *app) modesCommand() *cli.Command {
	return &cli.Command{
		Name:  "modes",
		Usage: "list available session modes",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			client, _, cleanup, err := a.dial()
			if err != nil {
				return err
			}
			defer cleanup()
			resp, err := client.ListModes(ctx, connect.NewRequest(&v1.ListModesRequest{}))
			if err != nil {
				return fmt.Errorf("ListModes: %w", err)
			}
			for _, mode := range resp.Msg.Modes {
				fmt.Printf("%-9s %s\n", mode.Name, mode.Description)
			}
			return nil
		},
	}
}

// stopCommand stops a running session.
func (a *app) stopCommand() *cli.Command {
	return &cli.Command{
		Name:      "stop",
		Usage:     "stop a running session",
		ArgsUsage: "<session-id>",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			id := cmd.Args().First()
			if id == "" {
				return fmt.Errorf("usage: ycc stop <session-id>")
			}
			client, _, cleanup, err := a.dial()
			if err != nil {
				return err
			}
			defer cleanup()
			if _, err := client.StopSession(ctx, connect.NewRequest(&v1.StopSessionRequest{SessionId: id})); err != nil {
				return fmt.Errorf("StopSession: %w", err)
			}
			fmt.Printf("stopped session %s\n", id)
			return nil
		},
	}
}

// projectCommand implements `ycc project <add|list|remove>` against the daemon's
// project registry (spec §3.1).
func (a *app) projectCommand() *cli.Command {
	return &cli.Command{
		Name:    "project",
		Aliases: []string{"projects"},
		Usage:   "manage the daemon's project registry",
		// `ycc project` with no subcommand lists, preserving prior behaviour.
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Present() {
				return fmt.Errorf("unknown project command %q", cmd.Args().First())
			}
			return a.projectList(ctx)
		},
		Commands: []*cli.Command{
			{
				Name:      "add",
				Usage:     "register a project directory",
				ArgsUsage: "<path>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "name", Usage: "project `name` (default: directory basename)"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					path := cmd.Args().First()
					if path == "" {
						return fmt.Errorf("usage: ycc project add <path> [--name N]")
					}
					if abs, err := filepath.Abs(path); err == nil {
						path = abs
					}
					client, _, cleanup, err := a.dial()
					if err != nil {
						return err
					}
					defer cleanup()
					resp, err := client.AddProject(ctx, connect.NewRequest(&v1.AddProjectRequest{Path: path, Name: cmd.String("name")}))
					if err != nil {
						return fmt.Errorf("AddProject: %w", err)
					}
					p := resp.Msg.Project
					fmt.Printf("registered %s  %s\n", p.Name, p.Path)
					return nil
				},
			},
			{
				Name:      "remove",
				Aliases:   []string{"rm"},
				Usage:     "remove a registered project",
				ArgsUsage: "<name>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					name := cmd.Args().First()
					if name == "" {
						return fmt.Errorf("usage: ycc project remove <name>")
					}
					client, _, cleanup, err := a.dial()
					if err != nil {
						return err
					}
					defer cleanup()
					if _, err := client.RemoveProject(ctx, connect.NewRequest(&v1.RemoveProjectRequest{Name: name})); err != nil {
						return fmt.Errorf("RemoveProject: %w", err)
					}
					fmt.Printf("removed %s\n", name)
					return nil
				},
			},
			{
				Name:  "list",
				Usage: "list registered projects",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return a.projectList(ctx)
				},
			},
		},
	}
}

func (a *app) projectList(ctx context.Context) error {
	client, _, cleanup, err := a.dial()
	if err != nil {
		return err
	}
	defer cleanup()
	resp, err := client.ListProjects(ctx, connect.NewRequest(&v1.ListProjectsRequest{}))
	if err != nil {
		return fmt.Errorf("ListProjects: %w", err)
	}
	if len(resp.Msg.Projects) == 0 {
		fmt.Println("(no projects)")
		return nil
	}
	for _, p := range resp.Msg.Projects {
		fmt.Printf("%-20s %s\n", p.Name, p.Path)
	}
	return nil
}

// costCommand renders the usage/cost breakdown (spec §20.3, §20.5) returned by
// the daemon's GetUsage RPC. By default it groups by backlog task; --by selects
// other dimensions (comma-separated: task,model,session,agent,day) and
// --since/--until bound an inclusive date range.
func (a *app) costCommand() *cli.Command {
	return &cli.Command{
		Name:  "cost",
		Usage: "show a usage/cost breakdown",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "project", Usage: "registered project `name` (default: daemon default workspace)"},
			&cli.StringFlag{Name: "by", Value: "task", Usage: "group by, comma-separated: task,model,session,agent,day"},
			&cli.StringFlag{Name: "since", Usage: "include usage on/after this day (`YYYY-MM-DD`)"},
			&cli.StringFlag{Name: "until", Usage: "include usage on/before this day (`YYYY-MM-DD`)"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			var groupBy []string
			for _, g := range strings.Split(cmd.String("by"), ",") {
				if g = strings.TrimSpace(g); g != "" {
					groupBy = append(groupBy, g)
				}
			}
			client, _, cleanup, err := a.dial()
			if err != nil {
				return err
			}
			defer cleanup()
			resp, err := client.GetUsage(ctx, connect.NewRequest(&v1.GetUsageRequest{
				Project: cmd.String("project"), GroupBy: groupBy, Since: cmd.String("since"), Until: cmd.String("until"),
			}))
			if err != nil {
				return fmt.Errorf("GetUsage: %w", err)
			}
			renderCost(resp.Msg, groupBy)
			return nil
		},
	}
}

// tokenCommand manages the machine-local LLM backend secrets store; it is a
// purely-local operation that does not need the daemon. A token can be saved
// once (keyed by its key_env name) instead of being exported in the environment
// every session.
func tokenCommand() *cli.Command {
	return &cli.Command{
		Name:  "token",
		Usage: "manage the machine-local secrets store (LLM backend / tool keys)",
		Commands: []*cli.Command{
			{
				Name:      "set",
				Usage:     "store a token, read from stdin (never from argv)",
				ArgsUsage: "<KEY_ENV>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					keyEnv := cmd.Args().First()
					if keyEnv == "" {
						return fmt.Errorf("usage: ycc token set <KEY_ENV>")
					}
					// Prompt only when stdin is a terminal; a piped value works unattended.
					if fi, err := os.Stdin.Stat(); err == nil && (fi.Mode()&os.ModeCharDevice) != 0 {
						fmt.Fprintf(os.Stderr, "enter token for %s (input hidden if piped): ", keyEnv)
					}
					r := bufio.NewReader(os.Stdin)
					line, err := r.ReadString('\n')
					if err != nil && line == "" {
						return fmt.Errorf("reading token: %w", err)
					}
					tok := strings.TrimSpace(line)
					if tok == "" {
						return fmt.Errorf("empty token, nothing stored")
					}
					if err := secrets.Set(keyEnv, tok); err != nil {
						return err
					}
					fmt.Printf("stored token for %s\n", keyEnv)
					return nil
				},
			},
			{
				Name:  "list",
				Usage: "list stored token key names",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					keys := secrets.Keys()
					if len(keys) == 0 {
						fmt.Println("(no stored tokens)")
						return nil
					}
					for _, k := range keys {
						fmt.Println(k)
					}
					return nil
				},
			},
			{
				Name:      "rm",
				Aliases:   []string{"remove"},
				Usage:     "remove a stored token",
				ArgsUsage: "<KEY_ENV>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					keyEnv := cmd.Args().First()
					if keyEnv == "" {
						return fmt.Errorf("usage: ycc token rm <KEY_ENV>")
					}
					if err := secrets.Remove(keyEnv); err != nil {
						return err
					}
					fmt.Printf("removed token for %s\n", keyEnv)
					return nil
				},
			},
		},
	}
}

// daemonCommand runs the explicit, persistent, foreground service. It serves
// until killed and does not dial a client of its own.
func daemonCommand() *cli.Command {
	return &cli.Command{
		Name:  "daemon",
		Usage: "run the explicit, persistent, foreground daemon",
		Description: "Runs the workspace daemon in the foreground until killed. It serves the\n" +
			"Connect API (session control + event streaming) that clients attach to.\n\n" +
			"Remote access (spec §12/§14): the deployment model is a private network\n" +
			"(Tailscale/VPN). A bearer --token is REQUIRED to bind any non-loopback\n" +
			"address — the daemon refuses to start on e.g. 0.0.0.0 or a tailnet IP\n" +
			"without one. TLS is optional: without --tls-cert/--tls-key a non-loopback\n" +
			"bind logs a cleartext warning (fine inside an encrypted tailnet). The token\n" +
			"may also be supplied via the YCC_TOKEN environment variable.\n\n" +
			"Clients attach with `ycc --addr <url> --token <t>` (or YCC_TOKEN), e.g.\n" +
			"`ycc --addr http://100.64.0.1:8787 --token $YCC_TOKEN list`. The same\n" +
			"endpoints are reachable over the Connect HTTP/JSON protocol with curl by\n" +
			"presenting the `Authorization: Bearer <token>` header.",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "addr", Value: "127.0.0.1:8787", Usage: "`address` to listen on (non-loopback requires --token)"},
			&cli.StringFlag{Name: "workspace", Value: ".", Usage: "default workspace for sessions that don't specify one"},
			&cli.StringFlag{Name: "config", Usage: "TOML config `file` (models + roles)"},
			&cli.StringFlag{Name: "model", Value: "claude-opus-4-8", Usage: "fallback model id (when no --config)"},
			&cli.StringFlag{Name: "base-url", Value: "https://api.anthropic.com", Usage: "fallback API base URL (when no --config)"},
			&cli.StringFlag{Name: "key-env", Value: "ANTHROPIC_API_KEY", Usage: "fallback API key env var (when no --config)"},
			&cli.IntFlag{Name: "max-tokens", Value: config.DefaultMaxTokens, Usage: "fallback max tokens per turn (when no --config)"},
			&cli.StringFlag{Name: "token", Sources: cli.EnvVars("YCC_TOKEN"), Usage: "bearer `token` clients must present; required for non-loopback binds (empty disables auth, loopback only)"},
			&cli.StringFlag{Name: "tls-cert", Usage: "TLS certificate `file` (enables HTTPS; optional on a private tailnet)"},
			&cli.StringFlag{Name: "tls-key", Usage: "TLS key `file`"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			err := daemon.Serve(daemon.Options{
				Addr:       cmd.String("addr"),
				Workspace:  cmd.String("workspace"),
				ConfigPath: cmd.String("config"),
				Model:      cmd.String("model"),
				BaseURL:    cmd.String("base-url"),
				KeyEnv:     cmd.String("key-env"),
				MaxTokens:  int(cmd.Int("max-tokens")),
				Token:      cmd.String("token"),
				TLSCert:    cmd.String("tls-cert"),
				TLSKey:     cmd.String("tls-key"),
				Persist:    true,
			})
			if err != nil {
				return fmt.Errorf("daemon: %w", err)
			}
			return nil
		},
	}
}

// resolveDaemon decides which daemon the client should talk to and returns its
// base URL, bearer token, whether it is a persistent/remote (multi-project)
// daemon, and a teardown func (a no-op for everything except a one-shot
// in-process daemon, which the caller must shut down on exit).
//
//   - --addr:        attach to the explicit/remote daemon.
//   - --background:  spawn (if needed) a detached persistent daemon and attach.
//   - default:       attach to a persistent local daemon if one is already
//     running, else start the daemon in-process on an ephemeral
//     address tied to this process (no persistence).
func resolveDaemon(addr, token string, background bool, ws, configPath string) (target, tok string, persistent bool, shutdown func(), err error) {
	noop := func() {}

	if addr != "" {
		return addr, token, true, noop, nil
	}

	if background {
		if err := daemon.EnsureBackgroundDaemon(ws, configPath); err != nil {
			return "", "", false, noop, err
		}
		return daemon.LocalAddr, "", true, noop, nil
	}

	// Attach to an already-running persistent local daemon if present.
	if daemon.Reachable(daemon.LocalAddr, "") {
		return daemon.LocalAddr, "", true, noop, nil
	}

	// Otherwise: one-shot in-process daemon on an ephemeral loopback address,
	// torn down when this process exits.
	if configPath == "" {
		configPath = daemon.DiscoverConfig(ws)
	}
	ip, err := daemon.StartInProcess(daemon.Options{
		Addr: "127.0.0.1:0", Workspace: ws, ConfigPath: configPath,
		Model: "claude-opus-4-8", BaseURL: "https://api.anthropic.com",
		KeyEnv: "ANTHROPIC_API_KEY", MaxTokens: config.DefaultMaxTokens,
	})
	if err != nil {
		return "", "", false, noop, fmt.Errorf("start in-process daemon: %w", err)
	}
	fmt.Fprintln(os.Stderr, "ycc: running one-shot in-process daemon (no persistence); closing ycc ends in-flight work.")
	fmt.Fprintln(os.Stderr, "ycc: use `ycc daemon` or `ycc --background` to keep work running after exit.")
	return ip.Addr, "", false, func() { _ = ip.Shutdown() }, nil
}

// installSignalShutdown runs the teardown hook on SIGINT/SIGTERM so a one-shot
// in-process daemon never survives a ctrl-c. After teardown it exits.
func installSignalShutdown(shutdown func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-ch
		shutdown()
		os.Exit(130)
	}()
}

func stream(ctx context.Context, client yccv1connect.SessionServiceClient, id string, from int64) error {
	s, err := client.Subscribe(ctx, connect.NewRequest(&v1.SubscribeRequest{SessionId: id, FromSeq: from}))
	if err != nil {
		return fmt.Errorf("Subscribe: %w", err)
	}
	for s.Receive() {
		printEvent(s.Msg())
	}
	if err := s.Err(); err != nil {
		return fmt.Errorf("stream: %w", err)
	}
	return nil
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

// renderCost renders the usage/cost table from a GetUsage response.
func renderCost(msg *v1.GetUsageResponse, groupBy []string) {
	if len(groupBy) == 0 {
		groupBy = []string{"task"}
	}
	if msg.Workspace != "" {
		fmt.Printf("usage breakdown for %s\n", msg.Workspace)
	}
	if len(msg.Rows) == 0 {
		fmt.Println("(no usage recorded)")
		return
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	header := make([]string, 0, len(groupBy)+5)
	for _, d := range groupBy {
		header = append(header, costTitle(d))
	}
	header = append(header, "Input", "Output", "Cache", "Total", "Cost")
	fmt.Fprintln(tw, strings.Join(header, "\t"))

	partial := false
	for _, r := range msg.Rows {
		fmt.Fprintln(tw, costRowLine(r, groupBy))
		if r.PriceStatus == "partial" {
			partial = true
		}
	}
	total := msg.Total
	if total != nil {
		cells := make([]string, len(groupBy))
		cells[0] = "TOTAL"
		cache := total.CacheRead + total.CacheWrite
		cells = append(cells, commas(total.Input), commas(total.Output), commas(cache), commas(total.Total), costCell(total))
		fmt.Fprintln(tw, strings.Join(cells, "\t"))
		if total.PriceStatus == "partial" {
			partial = true
		}
	}
	tw.Flush()
	if partial {
		fmt.Println("* partial pricing (some models unpriced)")
	}
}

func costRowLine(r *v1.UsageRow, groupBy []string) string {
	cells := make([]string, 0, len(groupBy)+5)
	for _, d := range groupBy {
		v := ""
		switch d {
		case "task":
			if v = r.Task; v == "" {
				v = "(unattributed)"
			}
		case "model":
			if v = r.Model; v == "" {
				v = "(unknown)"
			}
		case "session":
			v = r.Session
		case "agent":
			if v = r.Agent; v == "" {
				v = "(unknown)"
			}
		case "day":
			v = r.Day
		}
		cells = append(cells, v)
	}
	cache := r.CacheRead + r.CacheWrite
	cells = append(cells, commas(r.Input), commas(r.Output), commas(cache), commas(r.Total), costCell(r))
	return strings.Join(cells, "\t")
}

func costCell(r *v1.UsageRow) string {
	switch r.PriceStatus {
	case "unpriced":
		return "—"
	case "partial":
		return fmt.Sprintf("$%.4f*", r.Cost)
	default:
		return fmt.Sprintf("$%.4f", r.Cost)
	}
}

func costTitle(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// commas formats an int64 with thousands separators for cost tables.
func commas(n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var out []byte
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
