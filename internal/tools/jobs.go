package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/jobs"
)

// JobTools returns the background-job control tools (docs/design/async-jobs.md
// §3.2): job_output (non-blocking incremental read + status), wait (blocking
// final-report retrieval), and kill_job. They require ws.Jobs to be set; callers
// add them only when background jobs are enabled (see Editing).
func JobTools(ws *Workspace) []*gollama.Tool {
	return []*gollama.Tool{jobOutputTool(ws), waitTool(ws), killJobTool(ws)}
}

func jobOutputTool(ws *Workspace) *gollama.Tool {
	return &gollama.Tool{
		Name: "job_output",
		Description: "Peek at a background job's output SINCE YOU LAST READ IT, plus its current status " +
			"(running/done/failed/killed). Non-blocking; repeated calls return only NEW output. This does not " +
			"consume the job's final report — you still get that automatically or via wait. Use it to check on a " +
			"long-running job's progress, not to poll for completion.",
		Params: obj(map[string]any{"job_id": strProp("the job id, e.g. job_1")}, "job_id"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			id, ok := getString(params, "job_id")
			if !ok {
				return errResult("job_output: missing 'job_id'"), nil
			}
			job, ok := ws.Jobs.Get(id)
			if !ok {
				return errResult("job_output: no such job %q", id), nil
			}
			out, status := job.Read()
			body := out
			if strings.TrimSpace(body) == "" {
				// An agent job has no incremental output stream (its child-loop
				// events go to the log, not the job buffer); its report is delivered
				// exactly once via wait or checkpoint injection. Say so rather than
				// the bash-flavored "no new output" note.
				if job.Kind() == "agent" {
					body = "(agent job — no incremental output; its report arrives via wait or automatically)"
				} else {
					body = "(no new output since last read)"
				}
			}
			return okResult(fmt.Sprintf("job %s [%s]\n%s", id, status, body)), nil
		},
	}
}

func waitTool(ws *Workspace) *gollama.Tool {
	return &gollama.Tool{
		Name: "wait",
		Description: "Block until background job(s) finish, then return their final report(s) (exit code + output " +
			"tail). Pass job_ids to wait on specific jobs, or omit it to wait on all live jobs. 'for' selects any " +
			"(return as soon as one finishes) or all (default; wait for every target). Optional timeout_s bounds the " +
			"wait — on timeout you get the reports of whatever finished plus which jobs are still running (no error). " +
			"Call this only when a job's result gates your next step; otherwise keep working and the report arrives " +
			"automatically.",
		Params: obj(map[string]any{
			"job_ids":   StrArrProp("the job ids to wait on; omit to wait on all live jobs"),
			"for":       map[string]any{"type": "string", "enum": []string{"any", "all"}, "description": "return after any one finishes, or after all (default all)"},
			"timeout_s": map[string]any{"type": "integer", "description": "optional timeout in seconds; 0/absent = wait indefinitely"},
		}),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			ids := getStringSlice(params, "job_ids")
			mode := "all"
			if m, ok := getString(params, "for"); ok {
				mode = m
			}
			timeout := time.Duration(getInt(params, "timeout_s", 0)) * time.Second
			reports, running := ws.Jobs.Wait(ctx, ids, mode, timeout)
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
				fmt.Fprintf(&b, "still running: %s", strings.Join(running, ", "))
			}
			return okResult(b.String()), nil
		},
	}
}

func killJobTool(ws *Workspace) *gollama.Tool {
	return &gollama.Tool{
		Name:        "kill_job",
		Description: "Terminate a background job's process tree. Its status becomes 'killed'.",
		Params:      obj(map[string]any{"job_id": strProp("the job id to kill, e.g. job_1")}, "job_id"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			id, ok := getString(params, "job_id")
			if !ok {
				return errResult("kill_job: missing 'job_id'"), nil
			}
			job, ok := ws.Jobs.Get(id)
			if !ok {
				return errResult("kill_job: no such job %q", id), nil
			}
			if job.Kill() {
				// This call terminated it: emit job_finished (killed) exactly once,
				// tagged with the owning actor so drain/replay stay consistent.
				emitJobFinished(ws.Emitter, job.Owner(), job)
				return okResult(fmt.Sprintf("killed %s", id)), nil
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
