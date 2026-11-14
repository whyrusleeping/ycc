package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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
	Status    string        `json:"status"`
	Summary   string        `json:"summary"`
	Actions   []WatchAction `json:"actions,omitempty"`
	Notes     string        `json:"notes"`
	Completed bool          `json:"completed,omitempty"`
	Notify    bool          `json:"notify,omitempty"`
}

// WatchSpec is a full watch definition proposed from chat for creation or replacement.
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
			msg, err := d.WatchReport(r)
			if err != nil {
				return tools.ErrResult("watch_report: %v", err), nil
			}
			return &gollama.ToolResult{Content: msg, Structured: &tools.Control{Stop: true, Report: r.Summary}}, nil
		},
	}
}

func scheduleWatch(d *Deps) *gollama.Tool { return watchDefinitionTool(d, true) }
func updateWatch(d *Deps) *gollama.Tool   { return watchDefinitionTool(d, false) }

func watchDefinitionParams() gollama.ToolFunctionParams {
	return tools.Obj(map[string]any{
		"name":             tools.StrProp("watch name: 1-64 lowercase letters, digits, '-' or '_', starting with a letter or digit; updates cannot rename"),
		"brief":            tools.StrProp("self-contained per-run instructions: what to check, how, what counts as wrong, and when the work is complete"),
		"interval_minutes": map[string]any{"type": "integer", "minimum": 5, "description": "minutes between runs"},
		"model":            tools.StrProp("logical model for runs (default: coordinator)"),
		"probe":            tools.StrProp("shell command run first; exit 0 suppresses the entire model brief, so exit nonzero for problems OR awaited completion"),
		"probe_always":     tools.BoolProp("start a model run even when the probe passes, with its output"),
		"expires":          tools.StrProp("absolute RFC3339 pause time; copy definition.expires from get_watch to preserve expiry exactly; mutually exclusive with expires_hours; omit both for no expiry"),
		"expires_hours":    map[string]any{"type": "integer", "minimum": 1, "description": "pause after this many hours; mutually exclusive with expires; omit both for no expiry"},
		"allowed_actions":  tools.StrArrProp("riskier actions the user explicitly allowed"),
	}, "name", "brief", "interval_minutes")
}

func parseWatchName(params any) (string, error) {
	name, ok := tools.GetString(params, "name")
	name = strings.TrimSpace(name)
	if !ok || name == "" {
		return "", fmt.Errorf("name must be a nonempty string")
	}
	return name, nil
}

// watchDuration checks before conversion: GetInt alone truncates fractions and
// multiplication could wrap a large JSON number into a plausible duration.
func watchDuration(params any, key string, unit time.Duration, minimum int, optional bool) (time.Duration, error) {
	m, _ := params.(map[string]any)
	raw, present := m[key]
	if !present && optional {
		return 0, nil
	}
	var n float64
	switch v := raw.(type) {
	case int:
		n = float64(v)
	case float64:
		n = v
	default:
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < float64(minimum) || n > float64(math.MaxInt64/int64(unit)) {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", key, minimum, math.MaxInt64/int64(unit))
	}
	return time.Duration(n) * unit, nil
}

func parseWatchSpec(params any) (WatchSpec, error) {
	var s WatchSpec
	var err error
	if s.Name, err = parseWatchName(params); err != nil {
		return s, err
	}
	if s.Brief, _ = tools.GetString(params, "brief"); strings.TrimSpace(s.Brief) == "" {
		return s, fmt.Errorf("brief must be a nonempty string")
	}
	if s.Interval, err = watchDuration(params, "interval_minutes", time.Minute, 5, false); err != nil {
		return s, err
	}
	m, _ := params.(map[string]any)
	if absolute, present := m["expires"]; present {
		if _, relative := m["expires_hours"]; relative {
			return s, fmt.Errorf("expires and expires_hours are mutually exclusive")
		}
		value, ok := absolute.(string)
		if !ok {
			return s, fmt.Errorf("expires must be an RFC3339 string")
		}
		if s.Expires, err = time.Parse(time.RFC3339, value); err != nil {
			return s, fmt.Errorf("expires must be an RFC3339 timestamp: %w", err)
		}
	} else {
		expires, err := watchDuration(params, "expires_hours", time.Hour, 1, true)
		if err != nil {
			return s, err
		}
		if expires > 0 {
			s.Expires = time.Now().Add(expires)
		}
	}
	s.Model, _ = tools.GetString(params, "model")
	s.Probe, _ = tools.GetString(params, "probe")
	s.ProbeAlways = tools.GetBool(params, "probe_always", false)
	s.AllowedActions = tools.GetStringSlice(params, "allowed_actions")
	return s, nil
}

func watchDefinitionTool(d *Deps, creating bool) *gollama.Tool {
	name, mutate := "schedule_watch", d.ScheduleWatch
	description := "Create a new recurring watch only, never repair an existing definition by creating a companion. " +
		"Inspect existing watches first. The daemon runs fresh short sessions, not a continuation of this chat. " +
		"Use instead of sleep/wait loops for periodic checks and external work left running. The user confirms first. " +
		"Runs may take low-risk reversible actions; list riskier actions explicitly authorized in allowed_actions."
	if !creating {
		name, mutate = "update_watch", d.UpdateWatch
		description = "Explicit, user-confirmed FULL replacement of an existing watch definition, not a patch. " +
			"Inspect with get_watch first and preserve every needed field, including probe, model, expiry and allowed_actions; " +
			"omitted optional fields reset to defaults. name identifies the existing watch and cannot rename it. " +
			"Report history and continuity are retained; do not create a companion to correct a watch."
	}
	return &gollama.Tool{
		Name: name, Description: description, Params: watchDefinitionParams(),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			if mutate == nil {
				return tools.ErrResult("%s: watches are not available in this session", name), nil
			}
			spec, err := parseWatchSpec(params)
			if err == nil && d.ValidateWatch != nil {
				err = d.ValidateWatch(spec, creating)
			}
			if err != nil {
				return tools.ErrResult("%s: %v; no watch changed. Inspect the existing watch and use update_watch for corrections; do not create companion watches", name, err), nil
			}
			prompt := describeWatchSpec(spec)
			if !creating {
				prompt = "Replace the FULL definition of the existing watch (omitted optional fields reset to defaults).\n\n" +
					strings.Replace(prompt, "Create watch", "Update watch", 1)
			}
			ok, err := d.Asker.Confirm(ctx, prompt)
			if err != nil {
				return tools.ErrResult("%s: %v; no watch changed", name, err), nil
			}
			if !ok {
				return tools.ErrResult("%s: the user did not confirm; no watch changed", name), nil
			}
			msg, err := mutate(ctx, spec)
			if err != nil {
				return tools.ErrResult("%s: %v; no watch changed. Inspect the watch before retrying; use update_watch for corrections, not companion watches", name, err), nil
			}
			return tools.OkResult(msg), nil
		},
	}
}

func listWatches(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "list_watches", Description: "List existing watches before inspecting or correcting one.",
		Params: tools.Obj(map[string]any{}),
		Call: func(context.Context, any) (*gollama.ToolResult, error) {
			msg, err := d.ListWatches()
			if err != nil {
				return tools.ErrResult("list_watches: %v", err), nil
			}
			return tools.OkResult(msg), nil
		},
	}
}

func getWatch(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "get_watch", Description: "Inspect a watch's full definition, scheduling state, last report and continuity notes before editing it.",
		Params: tools.Obj(map[string]any{"name": tools.StrProp("existing watch name")}, "name"),
		Call: func(_ context.Context, params any) (*gollama.ToolResult, error) {
			name, err := parseWatchName(params)
			if err != nil {
				return tools.ErrResult("get_watch: %v", err), nil
			}
			msg, err := d.GetWatch(name)
			if err != nil {
				return tools.ErrResult("get_watch: %v", err), nil
			}
			return tools.OkResult(msg), nil
		},
	}
}

func setWatchPaused(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "set_watch_paused", Description: "Pause or resume an existing watch after inspecting its target and asking for user confirmation. Does not replace its definition.",
		Params: tools.Obj(map[string]any{
			"name": tools.StrProp("existing watch name"), "paused": tools.BoolProp("true to pause future checks; false to resume"),
		}, "name", "paused"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			name, err := parseWatchName(params)
			if err != nil {
				return tools.ErrResult("set_watch_paused: %v; no watch changed", err), nil
			}
			m, _ := params.(map[string]any)
			if _, ok := m["paused"].(bool); !ok {
				return tools.ErrResult("set_watch_paused: paused must be a boolean; no watch changed"), nil
			}
			paused := tools.GetBool(params, "paused", false)
			info, err := d.GetWatch(name)
			if err != nil {
				return tools.ErrResult("set_watch_paused: %v; no watch changed. Inspect the watch before retrying", err), nil
			}
			if !paused {
				var state struct {
					Definition struct {
						Expires time.Time `json:"expires"`
					} `json:"definition"`
				}
				if err := json.Unmarshal([]byte(info), &state); err != nil {
					return tools.ErrResult("set_watch_paused: cannot inspect expiry: %v; no watch changed", err), nil
				}
				if !state.Definition.Expires.IsZero() && !state.Definition.Expires.After(time.Now()) {
					return tools.ErrResult("set_watch_paused: watch has expired; no watch changed. Inspect it and update_watch its expiry before resuming"), nil
				}
			}
			action := "Pause"
			if !paused {
				action = "Resume"
			}
			ok, err := d.Asker.Confirm(ctx, fmt.Sprintf("%s watch %q? Its definition is unchanged.", action, name))
			if err != nil {
				return tools.ErrResult("set_watch_paused: %v; no watch changed", err), nil
			}
			if !ok {
				return tools.ErrResult("set_watch_paused: the user did not confirm; no watch changed"), nil
			}
			msg, err := d.SetWatchPaused(name, paused)
			if err != nil {
				return tools.ErrResult("set_watch_paused: %v", err), nil
			}
			return tools.OkResult(msg), nil
		},
	}
}

// describeWatchSpec renders the full definition, including defaults that an
// update would otherwise silently restore.
func describeWatchSpec(s WatchSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Create watch %q, running every %s?\n\nBrief:\n%s\n", s.Name, s.Interval, strings.TrimSpace(s.Brief))
	if s.Model != "" {
		fmt.Fprintf(&b, "\nModel: %s\n", s.Model)
	} else {
		b.WriteString("\nModel: coordinator (default)\n")
	}
	if s.Probe != "" {
		when := "a model run starts only on nonzero exit"
		if s.ProbeAlways {
			when = "a model run starts every time"
		}
		fmt.Fprintf(&b, "\nProbe (%s): %s\n", when, s.Probe)
	} else {
		b.WriteString("\nProbe: none (a model run starts every time)\n")
	}
	if !s.Expires.IsZero() {
		fmt.Fprintf(&b, "\nPauses at: %s\n", s.Expires.Format(time.RFC3339Nano))
	} else {
		b.WriteString("\nExpiry: none\n")
	}
	b.WriteString("\nRuns may take low-risk, reversible actions without asking")
	if len(s.AllowedActions) > 0 {
		b.WriteString(", plus:\n")
		for _, a := range s.AllowedActions {
			fmt.Fprintf(&b, "- %s\n", a)
		}
	} else {
		b.WriteString(".\nAdditional authorized actions: none.\n")
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

End every run with watch_report. An ordinary report ends only this run. When the awaited work is
finished, use completed=true to pause future checks and notify the user by default; notify=false
suppresses completion notification, and notify=true requires completed=true. This fresh session
does not resume the original chat or promise future commits from it. Record each action you took
with how to undo it. Normalize changes by elapsed time and compare only comparable baselines. If
you could not check something, say so rather than reporting it healthy.`
