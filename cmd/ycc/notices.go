package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	cli "github.com/urfave/cli/v3"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// noticesCommand implements `ycc notices [list|post|clear]`: the project's live
// sessions and notice board (experimental session coordination) from the shell.
func (a *app) noticesCommand() *cli.Command {
	projectFlag := func() *cli.StringFlag {
		return &cli.StringFlag{Name: "project", Usage: "registered project `name`"}
	}
	list := func(ctx context.Context, cmd *cli.Command) error {
		if cmd.Args().Present() {
			return fmt.Errorf("unknown notices command %q (run `ycc notices --help`)", cmd.Args().First())
		}
		client, cleanup, err := a.dial()
		if err != nil {
			return err
		}
		defer cleanup()
		resp, err := client.ListProjectNotices(ctx, connect.NewRequest(&v1.ListProjectNoticesRequest{Project: cmd.String("project")}))
		if err != nil {
			return fmt.Errorf("ListProjectNotices: %w", err)
		}
		renderNotices(os.Stdout, resp.Msg, time.Now())
		return nil
	}
	return &cli.Command{
		Name:  "notices",
		Usage: "list the project's live sessions and notice board; post or clear notices",
		Description: "Experimental session coordination: shows each live session's state, held tasks,\n" +
			"intent and claimed paths, and the active project notices. Empty while the\n" +
			"project's session coordination setting is off.",
		Flags:         []cli.Flag{projectFlag()},
		ShellComplete: a.completeWithProject(nil),
		Action:        list,
		Commands: []*cli.Command{
			{
				Name:          "list",
				Usage:         "list live sessions and active notices",
				Flags:         []cli.Flag{projectFlag()},
				ShellComplete: a.completeWithProject(nil),
				Action:        list,
			},
			{
				Name:          "post",
				Usage:         "post a notice as the user",
				ArgsUsage:     "\"text\"",
				ShellComplete: a.completeWithProject(nil),
				Flags: []cli.Flag{
					projectFlag(),
					&cli.DurationFlag{Name: "expires", Aliases: []string{"e"}, Usage: "expiry, 1m to 24h (required)"},
					&cli.StringFlag{Name: "subject", Aliases: []string{"s"}, Usage: "resource or topic, e.g. a service name"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					text := strings.TrimSpace(strings.Join(cmd.Args().Slice(), " "))
					if text == "" || cmd.Duration("expires") == 0 {
						return fmt.Errorf("usage: ycc notices post \"text\" --expires 2h [--subject S] [--project P]")
					}
					client, cleanup, err := a.dial()
					if err != nil {
						return err
					}
					defer cleanup()
					resp, err := client.PostProjectNotice(ctx, connect.NewRequest(&v1.PostProjectNoticeRequest{
						Project: cmd.String("project"), Text: text, Subject: cmd.String("subject"),
						ExpiresInSeconds: int64(cmd.Duration("expires") / time.Second),
					}))
					if err != nil {
						return fmt.Errorf("PostProjectNotice: %w", err)
					}
					fmt.Fprintf(os.Stdout, "posted %s, expires %s\n", resp.Msg.GetNotice().GetId(), localTime(resp.Msg.GetNotice().GetExpiresAt()))
					return nil
				},
			},
			{
				Name:          "clear",
				Usage:         "clear an active notice as the user",
				ArgsUsage:     "<id>",
				ShellComplete: a.completeWithProject(nil),
				Flags: []cli.Flag{
					projectFlag(),
					&cli.StringFlag{Name: "reason", Aliases: []string{"r"}, Usage: "why it no longer applies"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					id := strings.TrimSpace(cmd.Args().First())
					if id == "" || cmd.Args().Len() > 1 {
						return fmt.Errorf("usage: ycc notices clear <id> [--reason R] [--project P]")
					}
					client, cleanup, err := a.dial()
					if err != nil {
						return err
					}
					defer cleanup()
					if _, err := client.ClearProjectNotice(ctx, connect.NewRequest(&v1.ClearProjectNoticeRequest{
						Project: cmd.String("project"), Id: id, Reason: cmd.String("reason"),
					})); err != nil {
						return fmt.Errorf("ClearProjectNotice: %w", err)
					}
					fmt.Fprintf(os.Stdout, "cleared %s\n", id)
					return nil
				},
			},
		},
	}
}

// renderNotices prints the live sessions, then the active notices.
func renderNotices(out io.Writer, r *v1.ListProjectNoticesResponse, now time.Time) {
	var b strings.Builder
	if len(r.GetSessions()) == 0 {
		b.WriteString("Live sessions: none active in the last hour (or session coordination is off).\n")
	} else {
		b.WriteString("Live sessions:\n")
	}
	for _, s := range r.GetSessions() {
		fmt.Fprintf(&b, "- %s (%s, %s)", s.GetSessionId(), s.GetMode(), activityLabel(s.GetActivity()))
		if tasks := strings.Join(s.GetFocusTasks(), ", "); tasks != "" {
			if s.GetHeld() {
				b.WriteString(" holds " + tasks)
			} else {
				b.WriteString(" earlier focus " + tasks)
			}
		}
		if s.GetWorktree() != "" {
			b.WriteString(" in " + s.GetWorktree())
		}
		if n := s.GetRunningJobs(); n > 0 {
			fmt.Fprintf(&b, ", %d running job(s)", n)
		}
		b.WriteString("\n")
		if s.GetIntent() != "" {
			fmt.Fprintf(&b, "    intent: %s\n", s.GetIntent())
		}
		for _, l := range s.GetLeases() {
			fmt.Fprintf(&b, "    lease %s: %s, expires %s", l.GetName(), l.GetReason(), l.GetExpiresAt())
			if l.GetJobId() != "" {
				b.WriteString(" or when " + l.GetJobId() + " exits")
			}
			b.WriteString("\n")
		}
		if s.GetTitle() != "" {
			fmt.Fprintf(&b, "    title: %s\n", clipLine(s.GetTitle(), 120))
		}
		var definite, ambiguous []string
		for _, c := range s.GetClaims() {
			if c.GetAmbiguous() {
				ambiguous = append(ambiguous, c.GetPath())
			} else {
				definite = append(definite, c.GetPath())
			}
		}
		if len(definite) > 0 {
			fmt.Fprintf(&b, "    claims: %s\n", claimPathList(definite))
		}
		if len(ambiguous) > 0 {
			fmt.Fprintf(&b, "    ambiguous claims (possibly another session's): %s\n", claimPathList(ambiguous))
		}
	}
	if len(r.GetNotices()) == 0 {
		b.WriteString("Notice board: no active notices.\n")
	} else {
		b.WriteString("Notice board:\n")
	}
	for _, n := range r.GetNotices() {
		b.WriteString("- " + n.GetId())
		if n.GetSubject() != "" {
			fmt.Fprintf(&b, " [%s]", n.GetSubject())
		}
		poster := "the user"
		if n.GetSessionId() != "" {
			poster = n.GetSessionId()
			if len(n.GetTaskIds()) > 0 {
				poster += " (" + strings.Join(n.GetTaskIds(), ", ") + ")"
			}
		}
		fmt.Fprintf(&b, " %s\n    by %s, expires %s\n", n.GetText(), poster, expiryLabel(n.GetExpiresAt(), now))
	}
	fmt.Fprint(out, b.String())
}

// claimPathList joins claimed paths, the first five and a count of the rest.
func claimPathList(paths []string) string {
	const shown = 5
	if len(paths) > shown {
		return strings.Join(paths[:shown], ", ") + fmt.Sprintf(", … %d more", len(paths)-shown)
	}
	return strings.Join(paths, ", ")
}

func activityLabel(activity string) string {
	switch activity {
	case "question":
		return "waiting on a question"
	case "jobs":
		return "waiting on background work"
	}
	return activity
}

// expiryLabel renders an RFC 3339 expiry as local time plus time remaining.
func expiryLabel(ts string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	left := t.Sub(now).Round(time.Minute)
	if left < time.Minute {
		return t.Local().Format("15:04") + " (in under a minute)"
	}
	return fmt.Sprintf("%s (in %s)", t.Local().Format("15:04"), strings.TrimSuffix(left.String(), "0s"))
}

func localTime(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	return t.Local().Format("2006-01-02 15:04")
}

func clipLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
