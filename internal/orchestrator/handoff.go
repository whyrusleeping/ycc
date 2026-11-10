package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/tools"
)

// WorkHandoff is a user-approved hand-off of one backlog task from a chat to a
// new work session (start_work, spec §9). The chat stays open.
type WorkHandoff struct {
	TaskID string
	// Handoff is the bounded planning context (decisions, constraints,
	// verification expectations) carried into the work session's opening prompt.
	Handoff string
	// Workstream runs the work session in a separate workstream (its own
	// worktree and branch off the committed base) instead of the shared worktree.
	Workstream bool
}

// StartedWork describes the work session a hand-off started.
type StartedWork struct {
	SessionID  string
	Workstream string // workstream id when separate
	Branch     string // workstream branch when separate
	// Wait blocks until the work session finishes, goes idle, fails, or is
	// stopped, and returns its outcome. ok is false when ctx ended first.
	Wait func(ctx context.Context) (out WorkOutcome, ok bool)
}

// WorkOutcome is a handed-off work session's result, delivered to the chat as
// the final report of the job that tracks it.
type WorkOutcome struct {
	Failed bool
	Result string
}

// workHandoffPreviewBytes bounds the handoff excerpt shown in the confirmation.
const workHandoffPreviewBytes = 2000

// startWork hands one task to the work pipeline from a chat. It needs a real
// user confirmation, starts a separate work session, and tracks that session as
// a background job of the chat so its outcome is delivered like any other job.
func startWork(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "start_work",
		Description: "Hand one backlog task to the work pipeline: a new work session implements it, gets independent " +
			"proportional review, and commits it, while this chat stays open. Requires explicit user approval. The task " +
			"must be actionable (todo or in_progress with dependencies done). The handoff is the work session's only context " +
			"from this chat. Its outcome arrives automatically as a background job report.",
		Params: tools.Obj(map[string]any{
			"task_id": tools.StrProp("the exact backlog task id to implement, e.g. 0021"),
			"handoff": tools.StrProp("self-contained context for the work session: what was decided here, constraints, relevant " +
				"files or evidence, and the verification expected; bounded to 32 KiB"),
			"separate_workstream": map[string]any{"type": "boolean", "description": "run in a separate workstream (own worktree and " +
				"branch off the committed base, integrated after review) instead of this worktree. Use it when this chat or another " +
				"live session has uncommitted edits the task may touch, or when the user asks for isolation; uncommitted edits here " +
				"are not visible there, so the task file itself must be committed first"},
		}, "task_id", "handoff"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			if d.StartWork == nil {
				return tools.ErrResult("start_work: handing work to a work session is not available in this session"), nil
			}
			id, _ := tools.GetString(params, "task_id")
			handoff, _ := tools.GetString(params, "handoff")
			id, handoff = strings.TrimSpace(id), strings.TrimSpace(handoff)
			if id == "" {
				return tools.ErrResult("start_work: a target task_id is required"), nil
			}
			if len(handoff) > maxRevisionHandoffBytes {
				return tools.ErrResult("start_work: handoff is %d bytes; keep it under %d", len(handoff), maxRevisionHandoffBytes), nil
			}
			separate := tools.GetBool(params, "separate_workstream", false)
			t, err := d.Docs.Get(id)
			if err != nil {
				return tools.ErrResult("start_work: %v", err), nil
			}
			tasks, err := d.Docs.ListMetadata()
			if err != nil {
				return tools.ErrResult("start_work: cannot determine eligibility: %v", err), nil
			}
			eligibility := docs.EligibilityFor(t, docs.StatusByID(tasks))
			if _, requirement := taskEligibilityText(&eligibility, d.heldTasks()[t.ID]); !eligibility.Actionable {
				return tools.ErrResult("start_work: task %s is not actionable (%s)", t.ID, requirement), nil
			}

			where := "in this worktree"
			if separate {
				where = "in a separate workstream (its own worktree and branch off the committed base)"
			}
			question := fmt.Sprintf("Hand task %s (%s) to a new work session %s? It implements, reviews and commits the task "+
				"while this chat stays open.", t.ID, t.Title, where)
			if handoff != "" {
				question += "\n\nHandoff:\n" + truncate(handoff, workHandoffPreviewBytes)
			}
			ok, err := d.Asker.Confirm(ctx, question)
			if err != nil {
				return tools.ErrResult("start_work: %v", err), nil
			}
			if !ok {
				return tools.OkResult("The user declined; task " + t.ID + " was not handed off. Continue the conversation."), nil
			}
			started, err := d.StartWork(ctx, WorkHandoff{TaskID: t.ID, Handoff: handoff, Workstream: separate})
			if err != nil {
				return tools.ErrResult("start_work: %v", err), nil
			}
			record := map[string]any{"task": t.ID, "session": started.SessionID}
			if started.Workstream != "" {
				record["workstream"] = started.Workstream
				record["branch"] = started.Branch
			}
			d.Emitter.Emit(event.WorkHandoff, record)

			msg := "Started work session " + started.SessionID + " for task " + t.ID
			if started.Workstream != "" {
				msg += " in workstream " + started.Workstream + " (branch " + started.Branch + "; it integrates after review)"
			}
			msg += ". It runs independently; do not edit the files it is changing."
			if job := trackWorkSession(d, t.ID, started); job != nil {
				msg += fmt.Sprintf(" Its outcome arrives automatically as background job %s when it finishes, goes idle, or fails; "+
					"call wait([%q]) only if your next step depends on it. kill_job stops tracking it, not the work session.", job.ID(), job.ID())
			}
			return tools.OkResult(msg), nil
		},
	}
}

// trackWorkSession registers a chat background job that completes with the work
// session's outcome. Killing the job (or ending the chat) only stops tracking.
func trackWorkSession(d *Deps, taskID string, started StartedWork) *jobs.Job {
	if d.Jobs == nil || started.Wait == nil {
		return nil
	}
	job := d.Jobs.Start("session", "work session "+started.SessionID+" (task "+taskID+")", d.Emitter.Actor())
	if job == nil {
		return nil
	}
	d.Emitter.Emit(event.JobStarted, map[string]any{"id": job.ID(), "kind": job.Kind(), "label": job.Label(), "mutates": job.Mutates()})
	go func() {
		out, ok := started.Wait(job.Context())
		if !ok {
			return // killed or chat ended: that path owns the terminal report
		}
		status := jobs.Done
		if out.Failed {
			status = jobs.Failed
		}
		if job.Finish(status, out.Result) {
			emitAgentJobFinished(d.Emitter, job)
		}
	}()
	return job
}

// ChatHandoffPrompt is the opening prompt of a work session started by a chat's
// start_work: it pins the session to the task and carries the chat's handoff.
func ChatHandoffPrompt(chatSessionID, taskID, handoff string) string {
	p := "You are in work mode, started from chat session " + chatSessionID + " after the user approved handing off task " +
		taskID + ". Implement task " + taskID + " specifically — do NOT pick a different or \"next ready\" task. Read it " +
		"with get_task, set it in_progress, and drive it to a reviewed, committed state following the usual work flow. " +
		"The chat stays open and receives your final report."
	if strings.TrimSpace(handoff) != "" {
		p += "\n\nHandoff from the chat (planning context, decisions, verification expectations; refine as needed):\n" + handoff
	}
	return p
}

// workHandoffGuidance accompanies start_work in attended chat sessions.
const workHandoffGuidance = `When the user wants a backlog task implemented with independent review and a commit rather than
inline here, offer start_work: after the user confirms, a new work session implements exactly that
task from your handoff while this chat continues, and its outcome arrives as a background job report.
Use separate_workstream when this chat or another live session has uncommitted edits the task may
touch. Do not edit the files a handed-off session is changing.`
