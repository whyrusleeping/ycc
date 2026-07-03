package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/tools"
)

// CaptureDeps configures a one-shot, lightweight "quick-add backlog item"
// capture agent (task 0016, spec §18.2). It runs server-side off the main
// session's event stream, scoped to a single project's workspace, so a long
// `work` run is never disturbed by capturing a new backlog item mid-session.
type CaptureDeps struct {
	Workspace string
	Docs      *docs.Store
	Client    engine.Turner
	Model     string
	ModelName string
	Backend   string
	Thinking  engine.Thinking
	MaxTok    int
}

// CaptureResult is the outcome of a capture run: either a created task
// (TaskID/Title set) or a single clarifying question (Question set) the user
// must answer before the task can be created.
type CaptureResult struct {
	TaskID   string
	Title    string
	Question string
}

// capturePayload is the JSON the capture control tools stash in Control.Report
// so RunCapture can recover the structured outcome from the loop result.
type capturePayload struct {
	Kind     string `json:"kind"` // "created" | "question"
	TaskID   string `json:"task_id"`
	Title    string `json:"title"`
	Question string `json:"question"`
}

const captureSystem = `You are a backlog-capture assistant. Turn the user's short, natural-language description
into ONE well-formed backlog task. This is a QUICK capture, not planning: keep it FAST and do
minimal investigation. You MAY Read files and use list_backlog / get_task to ground the task and
avoid duplicates, but do not go deep. Prefer a couple of targeted reads and one backlog check over
a broad scan — do not open many files or run a full investigation.

Ask AT MOST ONE clarifying question — via ask_clarification — and ONLY if the description is too
vague to give the task a clear title and scope. Otherwise call create_task immediately with a
clear, imperative title, a short description, and acceptance criteria / priority / dependencies
when they are obvious. If the user has ALREADY answered a clarifying question, do NOT ask again —
create the task now.

Call create_task exactly once; it ends your work.`

// captureMaxTurns is the turn budget for the capture loop. It is the single
// source of truth used both for the loop's MaxTurns and the turn-budget
// sentence in the system prompt, so the two can't drift.
const captureMaxTurns = 32

// captureClarify is a control tool: it lets the capture agent ask ONE clarifying
// question, ending the loop with the question carried back to the client.
func captureClarify() *gollama.Tool {
	return &gollama.Tool{
		Name:        "ask_clarification",
		Description: "Ask the user ONE clarifying question when the description is too vague to title/scope the task. Use at most once, and only if necessary.",
		Params:      tools.Obj(map[string]any{"question": tools.StrProp("the single clarifying question for the user")}, "question"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			q, _ := tools.GetString(params, "question")
			payload, _ := json.Marshal(capturePayload{Kind: "question", Question: q})
			return &gollama.ToolResult{Content: "asked clarification", Structured: &tools.Control{Stop: true, Report: string(payload)}}, nil
		},
	}
}

// captureCreateTask mirrors createTask but, as a control tool, ends the capture
// loop with the new task id carried back to the client.
func captureCreateTask(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name:        "create_task",
		Description: "Create the new backlog task. Returns the assigned id and ends the capture. Call exactly once.",
		Params: tools.Obj(map[string]any{
			"title":       tools.StrProp("short, imperative task title"),
			"description": tools.StrProp("description and acceptance criteria (markdown)"),
			"priority":    map[string]any{"type": "integer", "description": "1 (highest) .. 5; default 3"},
			"depends_on":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "task ids this depends on"},
			"spec_refs":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "spec references this relates to: a bare section title refers to the spec entry point; `path#Section` references a section of another doc in the docs set"},
		}, "title"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			title, _ := tools.GetString(params, "title")
			desc, _ := tools.GetString(params, "description")
			body := ""
			if desc != "" {
				body = "## Description\n" + desc + "\n\n## Acceptance criteria\n\n## Work log\n"
			}
			t, err := d.Docs.Create(title, body, getInt(params, "priority", 3), getStrings(params, "depends_on"), getStrings(params, "spec_refs"))
			if err != nil {
				return tools.ErrResult("create_task: %v", err), nil
			}
			d.Emitter.Emit(event.DocUpdated, map[string]any{"task": t.ID, "created": true})
			payload, _ := json.Marshal(capturePayload{Kind: "created", TaskID: t.ID, Title: t.Title})
			return &gollama.ToolResult{Content: "created task " + t.ID + ": " + t.Title, Structured: &tools.Control{Stop: true, Report: string(payload)}}, nil
		},
	}
}

// RunCapture runs the lightweight capture agent to convert a natural-language
// description into a backlog task. It is a transient, off-stream agent: pass a
// real event.Recorder (e.g. event.NewFuncRecorder) to stream its action log to
// the caller, or nil to drop all events so the caller's main session stream is
// unaffected. If the agent asks a clarifying question, RunCapture returns it via
// Question; the client re-invokes with priorQuestion/priorAnswer so the agent
// creates the task without asking again.
func RunCapture(ctx context.Context, cd CaptureDeps, rec event.Recorder, description, priorQuestion, priorAnswer string) (CaptureResult, error) {
	emitter := event.NewEmitter(rec, "capture")
	ws := &tools.Workspace{Root: cd.Workspace, ReadRoots: tools.ReadRoots(nil)}
	d := &Deps{Workspace: cd.Workspace, Docs: cd.Docs, Emitter: emitter}

	reg := tools.New()
	reg.Add(tools.ReadOnly(ws)...)
	reg.Add(listBacklog(d), getTask(d), captureCreateTask(d), captureClarify())

	loop := &engine.Loop{
		Client:    cd.Client,
		Model:     cd.Model,
		ModelName: cd.ModelName,
		Backend:   cd.Backend,
		System: captureSystem +
			fmt.Sprintf("\n\nYou have a budget of %d turns (each is one model step that may include tool calls); "+
				"pace your quick investigation so you finish by calling create_task (or ask_clarification) "+
				"well before running out.", captureMaxTurns) +
			"\n\n" + workspaceNote(cd.Workspace),
		Tools:           reg,
		Emitter:         emitter,
		MaxTok:          cd.MaxTok,
		MaxTurns:        captureMaxTurns,
		Thinking:        cd.Thinking.Thinking,
		Effort:          cd.Thinking.Effort,
		ThinkingDisplay: cd.Thinking.ThinkingDisplay,
	}

	prompt := "Capture a new backlog item from this description:\n\n" + description
	if strings.TrimSpace(priorQuestion) != "" && strings.TrimSpace(priorAnswer) != "" {
		prompt += "\n\nYou previously asked: \"" + priorQuestion + "\"\nThe user answered: \"" + priorAnswer +
			"\"\n\nDo not ask anything further — create the task now with create_task."
	}
	loop.Seed(prompt)

	res, err := loop.Run(ctx)
	if err != nil {
		return CaptureResult{}, err
	}

	var p capturePayload
	if json.Unmarshal([]byte(res.Report), &p) == nil && p.Kind != "" {
		switch p.Kind {
		case "created":
			return CaptureResult{TaskID: p.TaskID, Title: p.Title}, nil
		case "question":
			return CaptureResult{Question: p.Question}, nil
		}
	}
	// Fallback: the agent yielded plain text instead of calling a control tool —
	// treat it as a clarifying question so the client can prompt the user.
	return CaptureResult{Question: strings.TrimSpace(res.Report)}, nil
}
