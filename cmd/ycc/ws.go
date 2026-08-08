package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"
	cli "github.com/urfave/cli/v3"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// wsCommand implements shell access to workstreams through the daemon. Both
// subcommands deliberately use ListWorkstreams so they work unchanged with a
// remote daemon selected by --addr.
func (a *app) wsCommand() *cli.Command {
	return &cli.Command{
		Name:    "ws",
		Aliases: []string{"workstream", "workstreams"},
		Usage:   "list workstreams and locate their worktrees",
		// `ycc ws` with no subcommand lists, matching `ycc project`.
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Present() {
				return fmt.Errorf("unknown ws command %q", cmd.Args().First())
			}
			return a.workstreamList(ctx, "", os.Stdout)
		},
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "list workstreams",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "project", Usage: "registered project `name`"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					if cmd.Args().Present() {
						return fmt.Errorf("usage: ycc ws list [--project P]")
					}
					return a.workstreamList(ctx, cmd.String("project"), os.Stdout)
				},
			},
			{
				Name:      "path",
				Usage:     "print a workstream's absolute worktree path",
				ArgsUsage: "<id>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					if cmd.Args().Len() != 1 || strings.TrimSpace(cmd.Args().First()) == "" {
						return fmt.Errorf("usage: ycc ws path <id>")
					}
					return a.workstreamPath(ctx, strings.TrimSpace(cmd.Args().First()), os.Stdout)
				},
			},
		},
	}
}

func (a *app) workstreamList(ctx context.Context, project string, out io.Writer) error {
	workstreams, err := a.listWorkstreams(ctx, project)
	if err != nil {
		return err
	}
	renderWorkstreamList(out, workstreams)
	return nil
}

func (a *app) workstreamPath(ctx context.Context, id string, out io.Writer) error {
	workstreams, err := a.listWorkstreams(ctx, "")
	if err != nil {
		return err
	}
	workstream, err := resolveWorkstreamID(workstreams, id)
	if err != nil {
		return err
	}
	if workstream.GetWorktreePath() == "" {
		return fmt.Errorf("workstream %q has no worktree path", workstream.GetId())
	}
	// Keep stdout machine-readable: this is intentionally the only output from
	// the successful path command so command substitution is safe.
	fmt.Fprintln(out, workstream.GetWorktreePath())
	return nil
}

func (a *app) listWorkstreams(ctx context.Context, project string) ([]*v1.WorkstreamInfo, error) {
	client, _, cleanup, err := a.dial()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	resp, err := client.ListWorkstreams(ctx, connect.NewRequest(&v1.ListWorkstreamsRequest{Project: project}))
	if err != nil {
		return nil, fmt.Errorf("ListWorkstreams: %w", err)
	}
	return resp.Msg.GetWorkstreams(), nil
}

// resolveWorkstreamID accepts a full workstream id or an unambiguous prefix.
// A bare prefix also matches the canonical ws_ namespace (3f9a -> ws_3f9a).
func resolveWorkstreamID(workstreams []*v1.WorkstreamInfo, arg string) (*v1.WorkstreamInfo, error) {
	for _, workstream := range workstreams {
		if workstream != nil && workstream.GetId() == arg {
			return workstream, nil
		}
	}

	prefixed := "ws_" + arg
	var matches []*v1.WorkstreamInfo
	for _, workstream := range workstreams {
		if workstream == nil {
			continue
		}
		id := workstream.GetId()
		if strings.HasPrefix(id, arg) || strings.HasPrefix(id, prefixed) {
			matches = append(matches, workstream)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("unknown workstream %q", arg)
	case 1:
		return matches[0], nil
	default:
		ids := make([]string, 0, len(matches))
		for _, match := range matches {
			ids = append(ids, match.GetId())
		}
		sort.Strings(ids)
		return nil, fmt.Errorf("ambiguous workstream %q (matches: %s)", arg, strings.Join(ids, ", "))
	}
}

// workstreamStatus mirrors the TUI panel's registry/session precedence. Terminal
// registry states and readiness win; otherwise a live session status refines an
// active workstream's display status.
func workstreamStatus(workstream *v1.WorkstreamInfo) string {
	switch workstream.GetStatus() {
	case "merged", "discarded", "stale":
		return workstream.GetStatus()
	case "needs_attention":
		return "⚠ needs attention"
	case "ready":
		return "ready"
	}
	if status := workstream.GetSessionStatus(); status != "" {
		return status
	}
	return workstream.GetStatus()
}

func renderWorkstreamList(out io.Writer, workstreams []*v1.WorkstreamInfo) {
	if len(workstreams) == 0 {
		fmt.Fprintln(out, "(no workstreams)")
		return
	}

	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTASK\tBRANCH\tCOMMITS\tSTATUS\tSESSION")
	for _, workstream := range workstreams {
		task := workstream.GetTaskId()
		if task == "" {
			task = "—"
		}
		branch := strings.TrimPrefix(workstream.GetBranch(), "ycc/ws/")
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d↑\t%s\t%s\n",
			workstream.GetId(), task, branch, workstream.GetCommitCount(),
			workstreamStatus(workstream), workstream.GetSessionId())
	}
	_ = tw.Flush()
}
