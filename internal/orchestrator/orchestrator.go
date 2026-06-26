// Package orchestrator implements the work-mode coordinator (spec §9, §10): an
// agent that reads the structured backlog, plans, and delegates real work to
// subagents (an implementer and one or more reviewers), then commits accepted
// work and updates the backlog. The coordinator never edits code itself — its
// tools spawn subagent loops and orchestrate them.
//
// M2 implements the happy path: pick a task, plan, implement, review once, and
// commit on accept. The revise loop and multi-model review arrive in M3 (0005).
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
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/tools"
)

const maxDiffChars = 16000

// Deps is everything the coordinator tools need to orchestrate a work session.
type Deps struct {
	Workspace     string
	Docs          *docs.Store
	Repo          *git.Repo
	Emitter       *event.Emitter // coordinator emitter (actor "coordinator")
	NewClient     func() engine.Turner
	Model         string
	ReviewerModel string // logical label for the single M2 reviewer (e.g. "claude")
	MaxTok        int
}

// CoordinatorSystem returns the coordinator's system prompt.
func CoordinatorSystem() string { return coordinatorSystem }

// CoordinatorTools returns the coordinator's tool registry.
func CoordinatorTools(d *Deps) *tools.Registry {
	reg := tools.New()
	reg.Add(
		listBacklog(d), getTask(d), proposePlan(d),
		spawnImplementer(d), spawnReviewer(d),
		commitTool(d), updateTask(d), tools.Finish(),
	)
	return reg
}

func listBacklog(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name:        "list_backlog",
		Description: "List all backlog tasks with id, status, priority, title, and dependencies.",
		Params:      tools.Obj(map[string]any{}),
		Call: func(ctx context.Context, _ any) (*gollama.ToolResult, error) {
			ts, err := d.Docs.List()
			if err != nil {
				return tools.ErrResult("list_backlog: %v", err), nil
			}
			if len(ts) == 0 {
				return tools.OkResult("(backlog is empty)"), nil
			}
			var b strings.Builder
			for _, t := range ts {
				dep := strings.Join(t.DependsOn, ",")
				if dep == "" {
					dep = "-"
				}
				fmt.Fprintf(&b, "%s [%s] p%d  %s  (deps: %s)\n", t.ID, t.Status, t.Priority, t.Title, dep)
			}
			return tools.OkResult(b.String()), nil
		},
	}
}

func getTask(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name:        "get_task",
		Description: "Read a single backlog task in full (frontmatter + description, acceptance criteria, work log).",
		Params:      tools.Obj(map[string]any{"task_id": tools.StrProp("task id, e.g. 0001")}, "task_id"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			id, _ := tools.GetString(params, "task_id")
			t, err := d.Docs.Get(id)
			if err != nil {
				return tools.ErrResult("get_task: %v", err), nil
			}
			return tools.OkResult(renderTask(t)), nil
		},
	}
}

func proposePlan(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name:        "propose_plan",
		Description: "Record your implementation plan for a task before delegating. Call once after choosing a task.",
		Params: tools.Obj(map[string]any{
			"task_id": tools.StrProp("task id"),
			"plan":    tools.StrProp("the implementation plan"),
		}, "task_id", "plan"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			id, _ := tools.GetString(params, "task_id")
			plan, _ := tools.GetString(params, "plan")
			d.Emitter.Emit(event.PlanProposed, map[string]any{"task": id, "plan": plan})
			if _, err := d.Docs.AppendWorkLog(id, "plan: "+oneLine(plan)); err != nil {
				return tools.ErrResult("propose_plan: %v", err), nil
			}
			return tools.OkResult("plan recorded"), nil
		},
	}
}

func spawnImplementer(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "spawn_implementer",
		Description: "Delegate implementation of a task to a coding subagent. It edits the workspace and returns a " +
			"report plus the staged diff of its changes. Provide the task id and your plan.",
		Params: tools.Obj(map[string]any{
			"task_id": tools.StrProp("task id"),
			"plan":    tools.StrProp("the plan the implementer should follow"),
		}, "task_id", "plan"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			id, _ := tools.GetString(params, "task_id")
			plan, _ := tools.GetString(params, "plan")
			t, err := d.Docs.Get(id)
			if err != nil {
				return tools.ErrResult("spawn_implementer: %v", err), nil
			}

			reg := tools.New()
			reg.Add(tools.Worker(&tools.Workspace{Root: d.Workspace})...)
			loop := &engine.Loop{
				Client:  d.NewClient(),
				Model:   d.Model,
				System:  implementerSystem,
				Tools:   reg,
				Emitter: d.Emitter.With("implementer"),
				MaxTok:  d.MaxTok,
			}
			loop.Seed(implementerPrompt(t, plan))

			d.Emitter.Emit(event.SubagentSpawned, map[string]any{"role": "implementer", "model": d.Model})
			res, err := loop.Run(ctx)
			if err != nil {
				d.Emitter.Emit(event.SubagentFinished, map[string]any{"role": "implementer", "error": err.Error()})
				return tools.ErrResult("implementer failed: %v", err), nil
			}
			diff, _ := d.Repo.Diff()
			d.Emitter.Emit(event.SubagentFinished, map[string]any{"role": "implementer"})
			d.Docs.AppendWorkLog(id, "implementer report: "+oneLine(res.Report))

			out := "IMPLEMENTER REPORT:\n" + res.Report + "\n\n=== STAGED DIFF ===\n" + truncate(diff, maxDiffChars)
			if strings.TrimSpace(diff) == "" {
				out += "(no changes were made to the workspace)"
			}
			return tools.OkResult(out), nil
		},
	}
}

func spawnReviewer(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "spawn_reviewer",
		Description: "Get an independent review of the implementer's changes for a task. The reviewer inspects the " +
			"diff and workspace and returns a verdict (accept/revise), a summary, and findings.",
		Params: tools.Obj(map[string]any{"task_id": tools.StrProp("task id")}, "task_id"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			id, _ := tools.GetString(params, "task_id")
			t, err := d.Docs.Get(id)
			if err != nil {
				return tools.ErrResult("spawn_reviewer: %v", err), nil
			}

			reg := tools.New()
			reg.Add(tools.Reviewer(&tools.Workspace{Root: d.Workspace})...)
			loop := &engine.Loop{
				Client:  d.NewClient(),
				Model:   d.Model,
				System:  reviewerSystem,
				Tools:   reg,
				Emitter: d.Emitter.With("reviewer:" + d.ReviewerModel),
				MaxTok:  d.MaxTok,
			}
			loop.Seed(reviewerPrompt(t))

			d.Emitter.Emit(event.SubagentSpawned, map[string]any{"role": "reviewer", "model": d.ReviewerModel})
			res, err := loop.Run(ctx)
			if err != nil {
				d.Emitter.Emit(event.SubagentFinished, map[string]any{"role": "reviewer", "error": err.Error()})
				return tools.ErrResult("reviewer failed: %v", err), nil
			}
			rv := parseReview(res.Report)
			d.Emitter.Emit(event.ReviewSubmitted, map[string]any{
				"model": d.ReviewerModel, "verdict": rv.Verdict, "summary": rv.Summary, "findings": len(rv.Findings),
			})
			d.Docs.AppendWorkLog(id, fmt.Sprintf("review (%s): %s — %s", d.ReviewerModel, rv.Verdict, oneLine(rv.Summary)))
			d.Emitter.Emit(event.SubagentFinished, map[string]any{"role": "reviewer"})
			return tools.OkResult(rv.render(d.ReviewerModel)), nil
		},
	}
}

func commitTool(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name:        "commit",
		Description: "Commit the accepted changes for a task to git. Records the decision in the task's work log.",
		Params: tools.Obj(map[string]any{
			"task_id": tools.StrProp("task id being committed"),
			"message": tools.StrProp("concise commit message"),
		}, "task_id", "message"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			id, _ := tools.GetString(params, "task_id")
			msg, _ := tools.GetString(params, "message")
			sha, err := d.Repo.Commit(msg)
			if err != nil {
				return tools.ErrResult("commit: %v", err), nil
			}
			d.Emitter.Emit(event.DecisionMade, map[string]any{"task": id, "decision": "accept"})
			d.Emitter.Emit(event.CommitMade, map[string]any{"task": id, "sha": sha, "message": msg})
			d.Docs.AppendWorkLog(id, fmt.Sprintf("decision: accept — commit %s: %s", sha, oneLine(msg)))
			return tools.OkResult("committed " + sha), nil
		},
	}
}

func updateTask(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name:        "update_task",
		Description: "Update a task's status (todo, in_progress, in_review, done, blocked) and regenerate the backlog index.",
		Params: tools.Obj(map[string]any{
			"task_id": tools.StrProp("task id"),
			"status":  map[string]any{"type": "string", "enum": []string{"todo", "in_progress", "in_review", "done", "blocked"}, "description": "new status"},
		}, "task_id", "status"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			id, _ := tools.GetString(params, "task_id")
			status, _ := tools.GetString(params, "status")
			if _, err := d.Docs.Update(id, func(t *docs.Task) { t.Status = docs.Status(status) }); err != nil {
				return tools.ErrResult("update_task: %v", err), nil
			}
			d.Docs.RenderIndex()
			d.Emitter.Emit(event.DocUpdated, map[string]any{"task": id, "status": status})
			return tools.OkResult(fmt.Sprintf("task %s -> %s", id, status)), nil
		},
	}
}

// --- review parsing ---

type finding struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type review struct {
	Verdict  string    `json:"verdict"`
	Summary  string    `json:"summary"`
	Findings []finding `json:"findings"`
}

// parseReview decodes a submit_review payload, tolerating a reviewer that yielded
// plain text instead of calling the tool.
func parseReview(report string) review {
	var rv review
	if err := json.Unmarshal([]byte(report), &rv); err == nil && rv.Verdict != "" {
		return rv
	}
	return review{Verdict: "unknown", Summary: strings.TrimSpace(report)}
}

func (rv review) render(model string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "REVIEW by %s\nverdict: %s\nsummary: %s\n", model, rv.Verdict, rv.Summary)
	for _, f := range rv.Findings {
		fmt.Fprintf(&b, "- [%s] %s\n", f.Severity, f.Message)
	}
	return b.String()
}

func renderTask(t *docs.Task) string {
	return fmt.Sprintf("id: %s\ntitle: %s\nstatus: %s\npriority: %d\ndepends_on: %s\nspec_refs: %s\n\n%s",
		t.ID, t.Title, t.Status, t.Priority, strings.Join(t.DependsOn, ","), strings.Join(t.SpecRefs, ","), t.Body)
}

func oneLine(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	return truncate(s, 200)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…[truncated]"
}
