package main

import (
	"context"
	"fmt"
	"os"

	"connectrpc.com/connect"
	cli "github.com/urfave/cli/v3"

	"github.com/whyrusleeping/ycc/internal/export"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// exportCommand renders a session's transcript to shareable markdown (task
// 0144): turns, collapsed tool-call summaries, folded ask_user Q&A, review
// verdicts, commits, the final report, and a usage/cost footer. It serves live
// and persisted sessions identically (GetSessionTranscript resolves both).
func (a *app) exportCommand() *cli.Command {
	return &cli.Command{
		Name:          "export",
		Usage:         "export a session transcript to shareable markdown",
		ArgsUsage:     "<session-id>",
		ShellComplete: a.completeWithProject(a.completeSessionIDs),
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "out", Usage: "write markdown to `FILE` (default: stdout)"},
			&cli.BoolFlag{Name: "full", Usage: "include tool call argument/result payloads"},
			&cli.StringFlag{Name: "project", Usage: "registered project `name` (default: daemon default workspace)"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			id := cmd.Args().First()
			if id == "" {
				return fmt.Errorf("usage: ycc export <session-id> [--out FILE] [--full]")
			}
			project := cmd.String("project")

			client, _, cleanup, err := a.dial()
			if err != nil {
				return err
			}
			defer cleanup()

			resp, err := client.GetSessionTranscript(ctx, connect.NewRequest(&v1.GetSessionTranscriptRequest{
				Project: project, SessionId: id,
			}))
			if err != nil {
				return fmt.Errorf("GetSessionTranscript: %w", err)
			}

			opts := export.Options{SessionID: id, Full: cmd.Bool("full")}
			// Best-effort usage/cost footer: filter GetUsage rows to this session.
			// Any failure just falls back to event-derived token totals.
			if ur, err := client.GetUsage(ctx, connect.NewRequest(&v1.GetUsageRequest{
				Project: project, GroupBy: []string{"session", "model"},
			})); err == nil {
				var rows []*v1.UsageRow
				for _, r := range ur.Msg.Rows {
					if r.Session == id {
						rows = append(rows, r)
					}
				}
				if len(rows) > 0 {
					opts.Usage = rows
				}
			}

			md := export.Markdown(resp.Msg.Events, opts)

			out := cmd.String("out")
			if out == "" {
				fmt.Print(md)
				return nil
			}
			if err := os.WriteFile(out, []byte(md), 0o644); err != nil {
				return fmt.Errorf("writing %s: %w", out, err)
			}
			fmt.Fprintf(os.Stderr, "wrote %s\n", out)
			return nil
		},
	}
}
