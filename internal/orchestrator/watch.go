package orchestrator

import (
	"context"
	"strings"

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
	Status    string        `json:"status"`
	Summary   string        `json:"summary"`
	Actions   []WatchAction `json:"actions,omitempty"`
	Notes     string        `json:"notes"`
	Completed bool          `json:"completed,omitempty"`
	Notify    bool          `json:"notify,omitempty"`
}

func watchReport(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "watch_report",
		Description: "End this watch run with its report. An ordinary report ends only this run; completed=true " +
			"pauses future checks when the awaited work is finished. Completion notifies the user by default; " +
			"set notify=false to suppress that notification. notify=true is allowed only with completed=true. " +
			"status: ok (nothing needs attention), warn (degraded, " +
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
			"notes":     tools.StrProp("complete replacement notes for the next run"),
			"completed": tools.BoolProp("the awaited work is finished: pause future checks (default false)"),
			"notify":    tools.BoolProp("notify the user of completion; default true when completed, false otherwise; true requires completed=true"),
		}, "status", "summary", "notes"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			var r WatchReport
			r.Status, _ = tools.GetString(params, "status")
			r.Summary, _ = tools.GetString(params, "summary")
			r.Notes, _ = tools.GetString(params, "notes")
			r.Completed = tools.GetBool(params, "completed", false)
			r.Notify = tools.GetBool(params, "notify", r.Completed)
			if r.Notify && !r.Completed {
				return tools.ErrResult("watch_report: notify=true requires completed=true; use status=alert for problems needing attention"), nil
			}
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
			// Commit the run's own backlog and memory lines now: nothing else
			// would, and left dirty they would ride along with a later commit.
			subject := singleLine(r.Summary)
			if len(subject) > 120 {
				subject = validUTF8Prefix(subject, 120) + "…"
			}
			committed, commitErr := commitBookkeeping(ctx, d, "Watch run notes: "+subject)
			if commitErr != nil && committed == "" {
				if r.Status != WatchAlert {
					return tools.ErrResult("watch_report: this run's backlog/memory edits could not be committed: %v. Fix the cause and call watch_report again, undo those edits, or report status=alert naming them for the user; nothing was recorded", commitErr), nil
				}
				r.Summary += "\nThis run's backlog/memory edits are uncommitted: " + commitErr.Error()
			}
			msg, err := d.WatchReport(r)
			if err != nil {
				return tools.ErrResult("watch_report: %v", err), nil
			}
			if committed != "" {
				msg += "\nCommitted this run's backlog/memory edits as " + committed + "."
			}
			if commitErr != nil && committed != "" {
				msg += "\n" + commitErr.Error()
			}
			return &gollama.ToolResult{Content: msg, Structured: &tools.Control{Stop: true, Report: r.Summary}}, nil
		},
	}
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

End every run with watch_report. An ordinary report ends only this run. When the awaited work is
finished, use completed=true to pause future checks and notify the user by default; notify=false
suppresses completion notification, and notify=true requires completed=true. This fresh session
does not resume the original chat or promise future commits from it. Record each action you took
with how to undo it. Normalize changes by elapsed time and compare only comparable baselines. If
you could not check something, say so rather than reporting it healthy.`
