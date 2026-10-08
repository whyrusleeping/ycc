package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	cli "github.com/urfave/cli/v3"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/whyrusleeping/ycc/internal/daemon"
	"github.com/whyrusleeping/ycc/internal/uianalytics"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// analyticsCommand implements `ycc analytics`: the client usage report
// (docs/design/usage-analytics.md). Like `ycc task`, it never spins up a
// one-shot daemon: it asks --addr or a reachable persistent local daemon, and
// otherwise reads the local analytics store directly.
func (a *app) analyticsCommand() *cli.Command {
	return &cli.Command{
		Name:  "analytics",
		Usage: "show which parts of the web/iOS/TUI clients are actually used",
		Description: "Summarises usage events the clients report to the daemon: screens and dwell time,\n" +
			"actions and how they were invoked, navigation paths, errors, abandoned flows, and\n" +
			"features never used. Raw events are JSONL under the printed storage directory.",
		Flags: []cli.Flag{
			&cli.IntFlag{Name: "days", Value: 30, Usage: "trailing window in `days`"},
			&cli.StringFlag{Name: "client", Usage: "only this client (web, ios, tui)"},
			&cli.IntFlag{Name: "top", Value: 25, Usage: "max rows per section (0 = all)"},
			&cli.BoolFlag{Name: "json", Usage: "print the raw report as JSON"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			days := int(cmd.Int("days"))
			if days <= 0 {
				return fmt.Errorf("--days must be positive")
			}
			msg, err := a.loadAnalytics(ctx, days, cmd.String("client"))
			if err != nil {
				return err
			}
			if cmd.Bool("json") {
				b, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(msg)
				if err != nil {
					return err
				}
				fmt.Println(string(b))
				return nil
			}
			renderAnalytics(os.Stdout, msg, int(cmd.Int("top")))
			return nil
		},
	}
}

func (a *app) loadAnalytics(ctx context.Context, days int, client string) (*v1.GetUiAnalyticsResponse, error) {
	req := &v1.GetUiAnalyticsRequest{Days: int32(days), Client: client}
	if a.addr != "" {
		resp, err := daemon.DialClient(a.addr, a.token).GetUiAnalytics(ctx, connect.NewRequest(req))
		if err != nil {
			return nil, fmt.Errorf("GetUiAnalytics: %w", err)
		}
		return resp.Msg, nil
	}
	if token, ok := daemon.ProbeLocal(a.token); ok {
		resp, err := daemon.DialClient(daemon.LocalAddr, token).GetUiAnalytics(ctx, connect.NewRequest(req))
		if err == nil && resp.Msg.Enabled {
			return resp.Msg, nil
		}
	}
	return uianalytics.OpenReadOnly(uianalytics.DefaultDir()).Report(days, client)
}

func renderAnalytics(w io.Writer, msg *v1.GetUiAnalyticsResponse, top int) {
	if !msg.Enabled {
		fmt.Fprintln(w, "usage analytics are not enabled on this daemon (only a persistent daemon stores them)")
		return
	}
	fmt.Fprintf(w, "client usage, last %d days (raw events: %s)\n", msg.Days, msg.StorageDir)
	if len(msg.Clients) == 0 {
		fmt.Fprintln(w, "(no events recorded)")
		return
	}
	limit := func(n int) int {
		if top > 0 && n > top {
			return top
		}
		return n
	}
	for _, c := range msg.Clients {
		fmt.Fprintf(w, "\n== %s: %d visits over %d active days, %d events (%s .. %s)\n",
			c.Client, c.Visits, c.ActiveDays, c.Events, fmtDay(c.FirstMs), fmtDay(c.LastMs))
		if len(c.Versions) > 0 {
			fmt.Fprintf(w, "versions: %s\n", strings.Join(c.Versions, ", "))
		}
		if len(c.VisitAttrs) > 0 {
			fmt.Fprintf(w, "visit context: %s\n", fmtCounts(c.VisitAttrs, 0))
		}
		var views, actions, errs, navs []*v1.UiAnalyticsRow
		for _, r := range msg.Rows {
			if r.Client != c.Client {
				continue
			}
			switch r.Kind {
			case "view":
				views = append(views, r)
			case "action":
				actions = append(actions, r)
			case "error":
				errs = append(errs, r)
			case "nav":
				navs = append(navs, r)
			}
		}
		if len(views) > 0 {
			fmt.Fprintf(w, "\nScreens\n")
			tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "  view\tcount\tdays\ttotal\tmedian\tlast\tarrived from")
			for _, r := range views[:limit(len(views))] {
				fmt.Fprintf(tw, "  %s\t%d\t%d\t%s\t%s\t%s\t%s\n", r.Name, r.Count, r.ActiveDays,
					fmtDur(r.TotalDurationMs), fmtDur(r.MedianDurationMs), fmtDay(r.LastMs), fmtCounts(r.Breakdown, 4))
			}
			tw.Flush()
		}
		if len(actions) > 0 {
			fmt.Fprintf(w, "\nActions\n")
			tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "  action\tcount\tdays\tlast\tvia\ton")
			for _, r := range actions[:limit(len(actions))] {
				fmt.Fprintf(tw, "  %s\t%d\t%d\t%s\t%s\t%s\n", r.Name, r.Count, r.ActiveDays, fmtDay(r.LastMs),
					fmtCounts(r.Breakdown, 4), fmtCounts(r.Contexts, 3))
			}
			tw.Flush()
		}
		if len(navs) > 0 {
			fmt.Fprintf(w, "\nNavigation paths\n")
			for _, r := range navs[:limit(len(navs))] {
				fmt.Fprintf(w, "  %-40s %d\n", strings.Replace(r.Name, ">", " -> ", 1), r.Count)
			}
		}
		if len(errs) > 0 {
			fmt.Fprintf(w, "\nErrors shown\n")
			for _, r := range errs[:limit(len(errs))] {
				fmt.Fprintf(w, "  %-32s %4d  codes: %s  on: %s\n", r.Name, r.Count, fmtCounts(r.Breakdown, 4), fmtCounts(r.Contexts, 3))
			}
		}
		var flows []*v1.UiFlow
		for _, f := range msg.Flows {
			if f.Client == c.Client {
				flows = append(flows, f)
			}
		}
		if len(flows) > 0 {
			fmt.Fprintf(w, "\nFlows (opened -> submitted / cancelled / abandoned)\n")
			for _, f := range flows {
				abandoned := f.Opened - f.Submitted - f.Cancelled
				if abandoned < 0 {
					abandoned = 0
				}
				fmt.Fprintf(w, "  %-32s %4d -> %d / %d / %d\n", f.Name, f.Opened, f.Submitted, f.Cancelled, abandoned)
			}
		}
		var unused, unlearned []string
		for _, g := range msg.Gaps {
			if g.Client != c.Client {
				continue
			}
			label := g.Kind + " " + g.Name
			if g.Shortcut != "" {
				label += " (" + g.Shortcut + ")"
			}
			if g.ShortcutNotLearned {
				unlearned = append(unlearned, fmt.Sprintf("%s, used %d times", label, g.Count))
			} else {
				unused = append(unused, label)
			}
		}
		if len(unused) > 0 {
			fmt.Fprintf(w, "\nNever used (%d)\n", len(unused))
			for _, u := range unused {
				fmt.Fprintf(w, "  %s\n", u)
			}
		}
		if len(unlearned) > 0 {
			fmt.Fprintf(w, "\nHas a shortcut but mostly invoked another way\n")
			for _, u := range unlearned {
				fmt.Fprintf(w, "  %s\n", u)
			}
		}
	}
}

// fmtCounts renders the largest n entries of m as "k:3 j:1" (0 = all).
func fmtCounts(m map[string]int64, n int) string {
	if len(m) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	more := 0
	if n > 0 && len(keys) > n {
		more = len(keys) - n
		keys = keys[:n]
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s:%d", k, m[k])
	}
	if more > 0 {
		parts = append(parts, fmt.Sprintf("+%d", more))
	}
	return strings.Join(parts, " ")
}

func fmtDur(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%.0fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%.0fm", d.Minutes())
	default:
		return fmt.Sprintf("%.1fh", d.Hours())
	}
}

func fmtDay(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return time.UnixMilli(ms).Local().Format("01-02")
}
