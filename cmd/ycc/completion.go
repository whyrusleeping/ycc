package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	cli "github.com/urfave/cli/v3"

	"github.com/whyrusleeping/ycc/internal/daemon"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

// completionTimeout bounds the RPCs issued while generating shell completions so
// a slow or wedged daemon never hangs the user's <tab>.
const completionTimeout = 1500 * time.Millisecond

// completionClient returns a SessionService client for dynamic shell completion,
// but ONLY when a daemon is already reachable without side effects: an explicit
// --addr, or an already-running persistent local daemon. It never starts the
// one-shot in-process daemon and never prints anything, so completion stays
// silent and fast when no daemon is around. Returns nil when none is reachable.
func (a *app) completionClient() yccv1connect.SessionServiceClient {
	if a.addr != "" {
		return daemon.DialClient(a.addr, a.token)
	}
	if daemon.Reachable(daemon.LocalAddr, "") {
		return daemon.DialClient(daemon.LocalAddr, "")
	}
	return nil
}

// completeSessionIDs is a ShellComplete func for commands whose leading
// positional is a <session-id> (attach, stop, export). It lists live session
// ids, strictly best-effort: with no reachable daemon or any RPC error it emits
// nothing. Only the first positional is completed. The emitted "id:desc" form is
// what both the bash and zsh completion scripts expect (they split on the first
// colon; session ids never contain one).
func (a *app) completeSessionIDs(ctx context.Context, cmd *cli.Command) {
	// Completing a flag (e.g. `ycc attach --f<tab>`): defer to the default
	// flag/subcommand suggester rather than emitting session ids.
	if strings.HasPrefix(prevCompletionArg(), "-") {
		cli.DefaultCompleteWithFlags(ctx, cmd)
		return
	}
	if cmd.Args().Present() {
		return // the session id is already present; nothing to complete
	}
	client := a.completionClient()
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, completionTimeout)
	defer cancel()
	resp, err := client.ListSessions(ctx, connect.NewRequest(&v1.ListSessionsRequest{}))
	if err != nil {
		return
	}
	for _, s := range resp.Msg.Sessions {
		fmt.Fprintf(cmd.Root().Writer, "%s:%s %s\n", s.SessionId, s.Mode, s.Status)
	}
}

// completeProjects emits the registered project names, best-effort (see
// completionClient / completeSessionIDs).
func (a *app) completeProjects(ctx context.Context, cmd *cli.Command) {
	client := a.completionClient()
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, completionTimeout)
	defer cancel()
	resp, err := client.ListProjects(ctx, connect.NewRequest(&v1.ListProjectsRequest{}))
	if err != nil {
		return
	}
	for _, p := range resp.Msg.Projects {
		fmt.Fprintln(cmd.Root().Writer, p.Name)
	}
}

// completeWithProject wraps a ShellComplete func so that when the token being
// completed follows a `--project` flag, registered project names are offered
// instead. Otherwise it delegates to next (or, when next is nil, to
// cli.DefaultCompleteWithFlags for the usual flag/subcommand suggestions).
func (a *app) completeWithProject(next cli.ShellCompleteFunc) cli.ShellCompleteFunc {
	return func(ctx context.Context, cmd *cli.Command) {
		if prevCompletionArg() == "--project" {
			a.completeProjects(ctx, cmd)
			return
		}
		if next != nil {
			next(ctx, cmd)
			return
		}
		cli.DefaultCompleteWithFlags(ctx, cmd)
	}
}

// prevCompletionArg returns the raw argv token immediately before the trailing
// `--generate-shell-completion` sentinel the shell scripts append — i.e. the
// word the user is completing after. It is "" when there is no such token.
func prevCompletionArg() string {
	args := os.Args
	if n := len(args); n >= 2 && args[n-1] == "--generate-shell-completion" {
		return args[n-2]
	}
	return ""
}
