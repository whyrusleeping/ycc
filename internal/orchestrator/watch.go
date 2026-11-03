package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/whyrusleeping/gollama"

	"github.com/whyrusleeping/ycc/internal/tools"
)

// WatchStatus values accepted by watch_report.
const (
	WatchOK    = "ok"
	WatchWarn  = "warn"
	WatchAlert = "alert"
)

// WatchNotesLimit bounds the notes a watch run passes to the next run.
const WatchNotesLimit = 4096

// WatchAction is one action a watch run took, with how to undo it.
type WatchAction struct {
	Action string `json:"action"`
	Undo   string `json:"undo,omitempty"`
}

// WatchReport is a watch run's structured result.
type WatchReport struct {
	Status  string        `json:"status"`
	Summary string        `json:"summary"`
	Actions []WatchAction `json:"actions,omitempty"`
	Notes   string        `json:"notes"`
}

// WatchSpec is a watch definition proposed from chat by schedule_watch.
type WatchSpec struct {
	Name           string
	Brief          string
	Interval       time.Duration
	Model          string
	Probe          string
	ProbeAlways    bool
	Expires        time.Time
	AllowedActions []string
}

func watchReport(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "watch_report",
		Description: "End this watch run with its report. status: ok (nothing needs attention), warn (degraded, " +
			"or you took an action), alert (needs the user now; ask_user first when a decision is needed). " +
			"notes replaces the notes the next run receives: keep baselines, open threads, and the user's " +
			"standing decisions that still apply, at most 4 KB.",
		Params: tools.Obj(map[string]any{
			"status":  map[string]any{"type": "string", "enum": []string{WatchOK, WatchWarn, WatchAlert}, "description": "overall status"},
			"summary": tools.StrProp("brief report for the user: what was checked, what changed, what needs attention"),
			"actions": map[string]any{
				"type":        "array",
				"description": "every action you took that changed anything, with how to undo it",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"action": tools.StrProp("what you did"),
						"undo":   tools.StrProp("how to reverse it"),
					},
					"required": []string{"action", "undo"},
				},
			},
			"notes": tools.StrProp("complete replacement notes for the next run"),
		}, "status", "summary", "notes"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			var r WatchReport
			r.Status, _ = tools.GetString(params, "status")
			r.Summary, _ = tools.GetString(params, "summary")
			r.Notes, _ = tools.GetString(params, "notes")
			r.Status = strings.TrimSpace(r.Status)
			switch r.Status {
			case WatchOK, WatchWarn, WatchAlert:
			default:
				return tools.ErrResult("watch_report: status must be ok, warn, or alert"), nil
			}
			if strings.TrimSpace(r.Summary) == "" {
				return tools.ErrResult("watch_report: summary is required"), nil
			}
			if len(r.Notes) > WatchNotesLimit {
				return tools.ErrResult("watch_report: notes are %d bytes; shorten them to at most %d", len(r.Notes), WatchNotesLimit), nil
			}
			for _, raw := range tools.GetMapSlice(params, "actions") {
				a, _ := tools.GetString(raw, "action")
				u, _ := tools.GetString(raw, "undo")
				if strings.TrimSpace(a) == "" {
					continue
				}
				if strings.TrimSpace(u) == "" {
					return tools.ErrResult("watch_report: action %q needs undo: say how to reverse it (or that it needs no reversal and why)", a), nil
				}
				r.Actions = append(r.Actions, WatchAction{Action: strings.TrimSpace(a), Undo: strings.TrimSpace(u)})
			}
			if d.WatchReport == nil {
				return tools.ErrResult("watch_report: this session is not a watch run"), nil
			}
			msg, err := d.WatchReport(r)
			if err != nil {
				return tools.ErrResult("watch_report: %v", err), nil
			}
			return &gollama.ToolResult{Content: msg, Structured: &tools.Control{Stop: true, Report: r.Summary}}, nil
		},
	}
}

func scheduleWatch(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "schedule_watch",
		Description: "Create a recurring watch: the daemon starts a fresh short session on a schedule to check " +
			"something (e.g. a live system), report, and ask the user when something needs attention. Use this " +
			"instead of sleep/wait loops when the user asks for periodic checks. The user confirms the definition " +
			"before it is created. Watches may take low-risk reversible actions unattended; list anything riskier " +
			"the user has explicitly allowed in allowed_actions.",
		Params: tools.Obj(map[string]any{
			"name":             tools.StrProp("short identifier: lowercase letters, digits, '-' or '_'"),
			"brief":            tools.StrProp("self-contained instructions for each run: what to check, how, and what counts as wrong"),
			"interval_minutes": map[string]any{"type": "integer", "minimum": 5, "description": "minutes between runs"},
			"model":            tools.StrProp("optional logical model for runs (default: the configured coordinator)"),
			"probe":            tools.StrProp("optional shell command the daemon runs first; exit 0 records ok without a model run"),
			"probe_always":     map[string]any{"type": "boolean", "description": "start a model run even when the probe passes, giving it the probe output"},
			"expires_hours":    map[string]any{"type": "integer", "minimum": 1, "description": "optional: pause the watch after this many hours"},
			"allowed_actions":  tools.StrArrProp("optional actions beyond the low-risk default that the user explicitly allowed"),
		}, "name", "brief", "interval_minutes"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			if d.ScheduleWatch == nil {
				return tools.ErrResult("schedule_watch: watches are not available in this session"), nil
			}
			var spec WatchSpec
			spec.Name, _ = tools.GetString(params, "name")
			spec.Brief, _ = tools.GetString(params, "brief")
			spec.Model, _ = tools.GetString(params, "model")
			spec.Probe, _ = tools.GetString(params, "probe")
			spec.ProbeAlways = tools.GetBool(params, "probe_always", false)
			spec.AllowedActions = tools.GetStringSlice(params, "allowed_actions")
			spec.Interval = time.Duration(tools.GetInt(params, "interval_minutes", 0)) * time.Minute
			if n := tools.GetInt(params, "expires_hours", 0); n > 0 {
				spec.Expires = time.Now().Add(time.Duration(n) * time.Hour)
			}
			ok, err := d.Asker.Confirm(ctx, describeWatchSpec(spec))
			if err != nil {
				return tools.ErrResult("schedule_watch: %v", err), nil
			}
			if !ok {
				return tools.ErrResult("schedule_watch: the user did not confirm; the watch was not created"), nil
			}
			msg, err := d.ScheduleWatch(ctx, spec)
			if err != nil {
				return tools.ErrResult("schedule_watch: %v", err), nil
			}
			return tools.OkResult(msg), nil
		},
	}
}

// describeWatchSpec renders the confirmation text for schedule_watch.
func describeWatchSpec(s WatchSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Create watch %q, running every %s?\n\nBrief:\n%s\n", s.Name, s.Interval, strings.TrimSpace(s.Brief))
	if s.Model != "" {
		fmt.Fprintf(&b, "\nModel: %s\n", s.Model)
	}
	if s.Probe != "" {
		when := "a model run starts only when it fails"
		if s.ProbeAlways {
			when = "a model run starts every time"
		}
		fmt.Fprintf(&b, "\nProbe (%s): %s\n", when, s.Probe)
	}
	if !s.Expires.IsZero() {
		fmt.Fprintf(&b, "\nPauses at: %s\n", s.Expires.Format(time.RFC3339))
	}
	b.WriteString("\nRuns may take low-risk, reversible actions without asking")
	if len(s.AllowedActions) > 0 {
		b.WriteString(", plus:\n")
		for _, a := range s.AllowedActions {
			fmt.Fprintf(&b, "- %s\n", a)
		}
	} else {
		b.WriteString(".\n")
	}
	return b.String()
}

const watchModeSystem = `You are one run of a recurring watch: a scheduled check the daemon starts in a fresh session.
The opening message is the user's brief; the daemon's run context follows it with notes from the
previous run, the user's standing decisions, pending questions, and a snapshot of other activity in
this project. Check what the brief asks, compare against the notes' baselines, and keep the run short.

Ask the user (ask_user) when something needs a decision; the schedule continues while you wait, and
you resume with the answer. Never repeat a pending question from an earlier run. Treat the user's
standing decisions as settled unless the situation has materially changed.

You may take low-risk actions without asking: reversible, and not deleting data, editing or
committing code, or stopping or restarting services. Actions listed as pre-authorized in the run
context are also allowed. Do not act on anything attributable to another live session or an
in_progress backlog task, including the processes, data, and services that task operates; ask
instead. When ownership is unclear, treat it as owned.

End every run with watch_report. Record each action you took with how to undo it. Normalize changes
by elapsed time and compare only comparable baselines. If you could not check something, say so
rather than reporting it healthy.`
