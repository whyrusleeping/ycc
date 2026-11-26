package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
)

// JobTools returns discovery, progress, repeatable evidence, synchronization,
// and cancellation tools for the session job registry.
func JobTools(ws *Workspace) []*gollama.Tool {
	return []*gollama.Tool{listJobsTool(ws), jobOutputTool(ws), waitTool(ws), killJobTool(ws)}
}

func jobToolOwner(ws *Workspace) string {
	if ws.Emitter == nil {
		return ""
	}
	return ws.Emitter.Actor()
}

func listJobsTool(ws *Workspace) *gollama.Tool {
	return &gollama.Tool{
		Name: "list_jobs",
		Description: "List this actor's live/completed jobs in start order: id, owner, state, elapsed, and activity. " +
			"include_all_owners selects the whole session. Listing does not affect notifications.",
		Params: obj(map[string]any{
			"include_all_owners": BoolProp("include jobs owned by other actors in this session (default false)"),
		}),
		Call: func(_ context.Context, params any) (*gollama.ToolResult, error) {
			infos := ws.Jobs.List(jobToolOwner(ws), getBool(params, "include_all_owners", false))
			if len(infos) == 0 {
				return okResult("list_jobs: no matching jobs."), nil
			}
			now := time.Now()
			var b strings.Builder
			for i, info := range infos {
				if i > 0 {
					b.WriteByte('\n')
				}
				end := info.Finished
				if end.IsZero() {
					end = now
				}
				elapsed := end.Sub(info.Started)
				if elapsed < 0 {
					elapsed = 0
				}
				fmt.Fprintf(&b, "%s [%s] kind=%s owner=%s elapsed=%s mutates=%t label=%q",
					info.ID, info.Status, info.Kind, info.Owner, elapsed.Round(time.Millisecond), info.Mutates, truncate(info.Label, 512))
				if info.Purpose != "" {
					fmt.Fprintf(&b, "\n  handoff: purpose=%q delivery=%q", truncate(info.Purpose, 512), info.Delivery)
				}
				if info.Kind == "agent" {
					activity := info.Activity
					fmt.Fprintf(&b, "\n  activity: turns=%d usage={input:%d output:%d cache_read:%d cache_write:%d total:%d}",
						activity.Turns, activity.Usage.Input, activity.Usage.Output, activity.Usage.CacheRead,
						activity.Usage.CacheWrite, activity.Usage.Total)
					if activity.CurrentTool != "" {
						fmt.Fprintf(&b, " current_tool=%q", truncate(activity.CurrentTool, 128))
					}
					if !activity.Last.IsZero() {
						ago := now.Sub(activity.Last)
						if ago < 0 {
							ago = 0
						}
						fmt.Fprintf(&b, " last_activity_ago=%s", ago.Round(time.Millisecond))
					}
				}
			}
			return okResult(b.String()), nil
		},
	}
}

func jobOutputTool(ws *Workspace) *gollama.Tool {
	return &gollama.Tool{
		Name: "job_output",
		Description: "Peek without affecting notifications. By default returns live output/activity or a completed job's " +
			"final report. Explicit cursor/limit or tail_lines read captured output, reporting ranges and eviction gaps. " +
			"Reads are repeatable; agent activity excludes model prose/reasoning.",
		Params: obj(map[string]any{
			"job_id":     strProp("the job id, e.g. job_1"),
			"cursor":     map[string]any{"type": "integer", "minimum": 0, "description": "absolute byte offset (default 0; explicit value selects captured output)"},
			"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": maxBashContentBytes, "description": "output byte budget (default about 63 KiB; also capped at 2000 lines)"},
			"tail_lines": map[string]any{"type": "integer", "minimum": 1, "maximum": maxBashLines, "description": "return the last N retained lines instead of a cursor range"},
		}, "job_id"),
		Call: func(_ context.Context, params any) (*gollama.ToolResult, error) {
			id, ok := getString(params, "job_id")
			if !ok {
				return errResult("job_output: missing 'job_id'"), nil
			}
			job, ok := ws.Jobs.Get(id)
			if !ok {
				return errResult("job_output: no such job %q", id), nil
			}
			if hasParam(params, "tail_lines") && hasParam(params, "cursor") {
				return errResult("job_output: use either cursor/limit or tail_lines, not both"), nil
			}
			if job.Status() != jobs.Running && !hasParam(params, "cursor") && !hasParam(params, "limit") && !hasParam(params, "tail_lines") {
				return okResult(FormatJobReport(job.Report())), nil
			}
			cursor := getInt(params, "cursor", 0)
			if cursor < 0 {
				cursor = 0
			}
			limit := getInt(params, "limit", maxBashContentBytes)
			if limit < 1 || limit > maxBashContentBytes {
				limit = maxBashContentBytes
			}
			tailLines := getInt(params, "tail_lines", 0)
			view := job.Output(int64(cursor), limit, tailLines)
			sourceEnd := len(view.Data)
			lineTruncated := false
			if tailLines == 0 {
				sourceEnd = jobOutputLineEnd(view.Data, maxBashLines)
				lineTruncated = sourceEnd < len(view.Data)
			}
			body, consumed := renderUTF8Range(view.Data, 0, sourceEnd, maxBashContentBytes)
			view.End = view.Start + int64(consumed)
			if strings.TrimSpace(body) == "" {
				if job.Kind() == "agent" {
					a := job.Info().Activity
					last := "never"
					if !a.Last.IsZero() {
						ago := time.Since(a.Last)
						if ago < 0 {
							ago = 0
						}
						last = ago.Round(time.Millisecond).String() + " ago"
					}
					body = fmt.Sprintf("(agent activity: turns=%d current_tool=%q last_activity=%s usage={input:%d output:%d cache_read:%d cache_write:%d total:%d})",
						a.Turns, a.CurrentTool, last, a.Usage.Input, a.Usage.Output, a.Usage.CacheRead, a.Usage.CacheWrite, a.Usage.Total)
				} else {
					body = "(no output in requested retained range)"
				}
			}
			var meta strings.Builder
			if view.GapEnd > view.GapStart {
				fmt.Fprintf(&meta, "\n[retention gap: bytes %d-%d were evicted]", view.GapStart, view.GapEnd)
			}
			if view.TailTruncated {
				meta.WriteString("\n[tail selection exceeded the response byte budget; showing its newest retained bytes]")
			}
			if lineTruncated {
				meta.WriteString("\n[range stopped at the 2000-line response budget; continue from next_cursor]")
			}
			fmt.Fprintf(&meta, "\n[output bytes %d-%d; retained %d-%d; next_cursor=%d; more=%t]",
				view.Start, view.End, view.RetainedStart, view.RetainedEnd, view.End, view.End < view.RetainedEnd)
			return okResult(fmt.Sprintf("job %s [%s]\n%s%s", id, job.Status(), body, meta.String())), nil
		},
	}
}

func jobOutputLineEnd(data []byte, maxLines int) int {
	lines := 0
	for i, b := range data {
		if b == '\n' {
			lines++
			if lines == maxLines {
				return i + 1
			}
		}
	}
	return len(data)
}

// defaultWaitTimeout bounds a wait call that doesn't pass timeout_s: a hung
// job must return control to the agent (with partial reports + a "still
// running" note) rather than block the loop forever. The agent can always
// wait again, check job_output, or kill_job.
const defaultWaitTimeout = 10 * time.Minute

func waitTool(ws *Workspace) *gollama.Tool {
	return &gollama.Tool{
		Name: "wait",
		Description: "Wait for jobs and return repeatable final reports, acknowledging completion so it is not notified " +
			"again. Specify ids, or omit for this actor's live jobs. Timeout returns completed reports plus running ids, " +
			"not an error. A killed report may precede actual execution cleanup. When no independent work remains, " +
			"wait rather than poll or end the turn. Put any message for the user in visible text beside this call; " +
			"reasoning is not shown.",
		Params: obj(map[string]any{
			"job_ids":   StrArrProp("the job ids to wait on; omit to wait on all live jobs"),
			"for":       map[string]any{"type": "string", "enum": []string{"any", "all"}, "description": "return after any one finishes (default), or after all; wait again for the rest"},
			"timeout_s": map[string]any{"type": "integer", "minimum": 1, "description": "timeout in seconds (default 600); on timeout you get partial reports plus the still-running ids, and can wait again"},
		}),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			ids := getStringSlice(params, "job_ids")
			for _, id := range ids {
				if _, ok := ws.Jobs.Get(id); !ok {
					return errResult("wait: no such job %q", id), nil
				}
			}
			if len(ids) == 0 {
				ids = ws.Jobs.LiveIDs(jobToolOwner(ws))
				// Registry.Wait historically interprets an empty target slice as all
				// registered jobs. The tool's omitted-id scope is narrower: only this
				// actor's live jobs. Do not accidentally expand an empty owner scope.
				if len(ids) == 0 {
					return okResult("wait: no matching jobs."), nil
				}
			}
			// Default "any": agents react to each completion (review, follow up,
			// cancel obsolete siblings) instead of idling until the slowest job.
			mode := "any"
			if m, ok := getString(params, "for"); ok {
				mode = m
			}
			timeout := time.Duration(getInt(params, "timeout_s", 0)) * time.Second
			if timeout <= 0 {
				timeout = defaultWaitTimeout
			}
			shellOnly := ws.ShellWaitCap > 0 && onlyShellJobs(ws.Jobs, ids)
			if shellOnly && timeout > ws.ShellWaitCap {
				timeout = ws.ShellWaitCap
			}
			spec := WaitSpec{IDs: ids, Mode: mode, Deadline: time.Now().Add(timeout), Timeout: timeout, ShellOnly: shellOnly}
			// Recorded before blocking, so a reopen whose log ends inside this
			// wait resumes it on the same targets and deadline
			// (docs/design/daemon-restart.md §4).
			if ws.Emitter != nil {
				ws.Emitter.Emit(event.WaitStarted, spec.EventData(ToolCallID(ctx)))
			}
			return runWait(ctx, ws, spec, timeout, false)
		},
	}
}

// WaitSpec is a wait call's resolved targets and effective deadline, as
// recorded on its wait_started event.
type WaitSpec struct {
	IDs      []string
	Mode     string // "any" or "all"
	Deadline time.Time
	// Timeout is the call's effective timeout (default and work-loop shell cap
	// applied), quoted in its result when jobs are still running.
	Timeout   time.Duration
	ShellOnly bool
}

// EventData is the wait_started payload for the wait call callID.
func (s WaitSpec) EventData(callID string) map[string]any {
	return map[string]any{
		"call_id":    callID,
		"job_ids":    append([]string(nil), s.IDs...),
		"for":        s.Mode,
		"deadline":   s.Deadline.UTC().Format(time.RFC3339Nano),
		"timeout_ms": s.Timeout.Milliseconds(),
		"shell_only": s.ShellOnly,
	}
}

// ParseWaitStarted decodes a wait_started payload, reporting false when it
// lacks a call id, targets, or a valid deadline.
func ParseWaitStarted(data map[string]any) (callID string, spec WaitSpec, ok bool) {
	callID, _ = data["call_id"].(string)
	switch ids := data["job_ids"].(type) {
	case []string:
		spec.IDs = append([]string(nil), ids...)
	case []any:
		for _, v := range ids {
			if id, isStr := v.(string); isStr && id != "" {
				spec.IDs = append(spec.IDs, id)
			}
		}
	}
	spec.Mode, _ = data["for"].(string)
	if spec.Mode != "all" {
		spec.Mode = "any"
	}
	deadline, _ := data["deadline"].(string)
	t, err := time.Parse(time.RFC3339Nano, deadline)
	if err != nil || callID == "" || len(spec.IDs) == 0 {
		return "", WaitSpec{}, false
	}
	spec.Deadline = t
	switch v := data["timeout_ms"].(type) {
	case float64:
		spec.Timeout = time.Duration(v) * time.Millisecond
	case int64:
		spec.Timeout = time.Duration(v) * time.Millisecond
	case int:
		spec.Timeout = time.Duration(v) * time.Millisecond
	}
	spec.ShellOnly, _ = data["shell_only"].(bool)
	return callID, spec, true
}

// ResumeWait finishes a wait call restored from its wait_started event after a
// reopen: it waits on the same targets until the recorded deadline, or — when
// the deadline has already passed — collects what is ready without blocking.
// Reports a previous run already claimed are returned again without a second
// job_claimed. It returns jobs.ErrRestartSuspend when a graceful restart
// suspends it again.
func ResumeWait(ctx context.Context, ws *Workspace, spec WaitSpec, now time.Time) (*gollama.ToolResult, error) {
	remaining := spec.Deadline.Sub(now)
	if remaining <= 0 {
		return runWait(ctx, ws, spec, 0, true)
	}
	return runWait(ctx, ws, spec, remaining, false)
}

// runWait blocks on spec's targets for timeout (or, when collectNow is set,
// only collects without blocking), records a job_claimed for each report it
// claims, and renders the wait result. A user message queued as a correction
// ends the wait early.
func runWait(ctx context.Context, ws *Workspace, spec WaitSpec, timeout time.Duration, collectNow bool) (*gollama.ToolResult, error) {
	waitCtx, wake := ctx, (<-chan struct{})(nil)
	if ws.InputWake != nil {
		wake = ws.InputWake()
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithCancel(ctx)
		defer cancel()
		go func() {
			select {
			case <-wake:
				cancel()
			case <-waitCtx.Done():
			}
		}()
	}
	var (
		reports   []jobs.Report
		running   []string
		suspended bool
	)
	if collectNow {
		reports, running, suspended = ws.Jobs.CollectSuspendable(waitCtx, spec.IDs)
	} else {
		reports, running, suspended = ws.Jobs.WaitSuspendable(waitCtx, spec.IDs, spec.Mode, timeout)
	}
	if suspended {
		// Suspended by a graceful daemon restart: nothing was claimed, so
		// record no result and reopen restores this wait.
		return nil, jobs.ErrRestartSuspend
	}
	interrupted := false
	if len(running) > 0 && wake != nil {
		select {
		case <-wake:
			interrupted = true
		default:
		}
	}
	if ws.Emitter != nil {
		for _, report := range reports {
			if report.ClaimedNotification {
				ws.Emitter.Emit(event.JobClaimed, map[string]any{
					"id": report.ID, "kind": report.Kind, "label": report.Label,
					"status": string(report.Status), "result": report.Result, "reason": "wait",
				})
			}
		}
	}
	if len(reports) == 0 && len(running) == 0 {
		return okResult("wait: no matching jobs."), nil
	}
	var b strings.Builder
	for i, r := range reports {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(FormatJobReport(r))
	}
	if len(running) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		if interrupted {
			fmt.Fprintf(&b, "wait returned early because the user sent a message (it follows); still running: %s. "+
				"Answer the user in visible text (reasoning is not shown), then wait again or end your turn: "+
				"their completion is still notified automatically and wakes this session",
				strings.Join(running, ", "))
		} else if spec.ShellOnly {
			fmt.Fprintf(&b, "still running after %s: %s. This work-loop session holds a working slot while it "+
				"waits. If nothing else remains, report progress in visible text and end your turn: the job's "+
				"completion resumes you, and meanwhile the loop counts you as waiting and starts other work",
				spec.Timeout, strings.Join(running, ", "))
		} else {
			fmt.Fprintf(&b, "still running after %s: %s (wait again, check job_output, or kill_job)",
				spec.Timeout, strings.Join(running, ", "))
		}
	}
	return okResult(b.String()), nil
}

// onlyShellJobs reports whether every listed job is a background shell job
// (not a subagent run), the case in which an idle loop session counts as waiting.
func onlyShellJobs(reg *jobs.Registry, ids []string) bool {
	for _, id := range ids {
		if job, ok := reg.Get(id); !ok || job.Kind() == "agent" {
			return false
		}
	}
	return len(ids) > 0
}

func killJobTool(ws *Workspace) *gollama.Tool {
	return &gollama.Tool{
		Name: "kill_job",
		Description: "Cancel a running job: terminate a shell process tree or cancel a subagent's current run. " +
			"Marks it killed immediately, but cleanup may still be unwinding. Does not undo completed work " +
			"or delete the agent handle. Follow-ups are refused until that run stops.",
		Params: obj(map[string]any{"job_id": strProp("the job id to kill, e.g. job_1")}, "job_id"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			id, ok := getString(params, "job_id")
			if !ok {
				return errResult("kill_job: missing 'job_id'"), nil
			}
			job, ok := ws.Jobs.Get(id)
			if !ok {
				return errResult("kill_job: no such job %q", id), nil
			}
			if job.ExecutionUnknown() {
				return errResult("kill_job: supervision of job %s was lost, so its process could not be confirmed stopped "+
					"and may still be running; ycc can no longer signal it", id), nil
			}
			if job.Kill() {
				// This call terminated it: emit job_finished (killed) exactly once,
				// tagged with the owning actor so drain/replay stay consistent.
				emitJobFinished(ws.Emitter, job.Owner(), job)
				if err := job.KillError(); err != nil {
					return okResult(fmt.Sprintf("marked %s killed, but the kill request could not reach its supervisor (%v): "+
						"the process could not be confirmed stopped and may still be running\n%s", id, err, job.Report().Result)), nil
				}
				return okResult(fmt.Sprintf("killed %s\n%s", id, job.Report().Result)), nil
			}
			return okResult(fmt.Sprintf("job %s was already %s", id, job.Status())), nil
		},
	}
}

// FormatJobReport renders a job's final report for a wait tool-result or a
// checkpoint-injected notification: a "[job <id> <status>] <label>" header
// followed by the exit line + output tail.
func FormatJobReport(r jobs.Report) string {
	head := fmt.Sprintf("[job %s %s] %s", r.ID, r.Status, r.Label)
	if strings.TrimSpace(r.Result) == "" {
		return head
	}
	return head + "\n" + r.Result
}
