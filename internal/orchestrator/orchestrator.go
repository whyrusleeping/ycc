// Package orchestrator implements the work-mode coordinator (spec §9, §10): an
// agent that reads the structured backlog, plans, and delegates real work to
// subagents (an implementer and one or more reviewers), then commits accepted
// work and updates the backlog. The coordinator never edits code itself.
//
// M3 adds: multi-model reviewer fan-out (concurrent, with a barrier), a revise
// loop that REUSES subagent contexts (send_to_implementer / re_review), and the
// three interaction levels gating ask_user.
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/tools"
)

const maxDiffChars = 16000

// implementerMinTok is the floor on the implementer's per-turn output token cap.
// The implementer reasons (extended thinking) and writes large multi-file edits
// in the same turn, both drawing on this budget; a low cap truncates the turn
// before a tool call lands. It only raises the configured cap, never lowers it.
const implementerMinTok = 16384

// AgentSpec describes how to build a subagent's backend.
type AgentSpec struct {
	Name      string // logical name, used as the actor label "reviewer:<name>"
	NewClient func() engine.Turner
	Model     string
	Backend   string // logical backend family (e.g. "anthropic"); labels usage events
	// Thinking carries the per-model reasoning settings (Anthropic extended
	// thinking / effort) so spawned subagents reason like the coordinator does
	// (spec §7, §13). Zero value means reasoning is off for this model.
	Thinking        string
	Effort          string
	ThinkingDisplay string
}

// Question is one prompt in a batch ask_user call, with its own optional set of
// suggested answers.
type Question struct {
	Prompt  string
	Options []string
}

// Asker lets the coordinator ask the user a question, subject to the session's
// interaction level. Implemented by the session.
type Asker interface {
	Ask(ctx context.Context, question string, options []string) (string, error)
	// AskMany poses several questions in a single round-trip, each with its own
	// optional set of suggested answers. The returned slice is parallel to the
	// input: answers[i] is the answer to questions[i]. Subject to the same
	// interaction-level gating as Ask (autonomous mode auto-answers each).
	AskMany(ctx context.Context, questions []Question) ([]string, error)
	// Confirm asks the user a yes/no question for a high-impact, hard-to-reverse
	// action (e.g. starting the work pipeline). Unlike Ask, it requires a real
	// human answer even in autonomous mode: when no human is available it returns
	// (false, nil) so the action is declined rather than silently taken (spec §9, §11).
	Confirm(ctx context.Context, question string) (bool, error)
}

// ReviewPlan is the resolved review approach for one spawn_reviewers call: which
// reviewer agents to spawn, or (SelfReview) that the coordinator reviews the
// change itself (the 'simple' tier). It is produced by Deps.ReviewTier.
type ReviewPlan struct {
	Tier       string      // effective tier name used
	SelfReview bool        // simple tier: coordinator self-reviews; no agents spawned
	Specs      []AgentSpec // reviewer agents to spawn (empty when SelfReview)
	Requested  string      // tier the coordinator requested (for auditing)
	Fallback   bool        // requested tier was unknown; degraded to default
}

// Deps is everything the coordinator tools need to orchestrate a work session.
// It also holds the live subagent handles so the revise loop can reuse their
// conversation contexts across rounds.
type Deps struct {
	Workspace   string
	Docs        *docs.Store
	Repo        *git.Repo
	Emitter     *event.Emitter // coordinator emitter (actor "coordinator")
	Implementer AgentSpec
	Reviewers   []AgentSpec
	Asker       Asker
	MaxTok      int
	MaxTurns    int // per-Run tool-call turn cap; 0 => engine default backstop
	// ReviewTier resolves a requested review tier name (possibly empty) into a
	// concrete ReviewPlan — which reviewer agents to spawn, or that the
	// coordinator self-reviews (spec §13). Nil-safe: when unset, spawn_reviewers
	// falls back to the configured reviewer fan-out (current default behaviour).
	ReviewTier func(name string) ReviewPlan

	mu        sync.Mutex
	impl      *engine.Loop
	reviewers []*reviewerHandle
	focus     string // backlog task currently in focus (spec §20.2); guarded by mu
}

// emitFocus records a task_focus event when the session's active focus moves to a
// new task (spec §20.2), durably linking the session to the task so usage can be
// attributed by backlog task. It dedupes: re-focusing the already-focused task is
// a no-op, so the log isn't littered with duplicate focus events for one task.
func (d *Deps) emitFocus(taskID string) {
	id := strings.TrimSpace(taskID)
	if id == "" {
		return
	}
	d.mu.Lock()
	if d.focus == id {
		d.mu.Unlock()
		return
	}
	d.focus = id
	d.mu.Unlock()
	d.Emitter.Emit(event.TaskFocus, map[string]any{"task": id})
}

type reviewerHandle struct {
	name string
	loop *engine.Loop
}

// SetImplementer swaps the implementer spec used by future spawn_implementer
// calls (mid-session role-config change, spec §18.2). The currently-running
// implementer keeps its context until the next fresh spawn.
func (d *Deps) SetImplementer(spec AgentSpec) {
	d.mu.Lock()
	d.Implementer = spec
	d.mu.Unlock()
}

// SetReviewers swaps the reviewer specs used by the next spawn_reviewers call.
func (d *Deps) SetReviewers(specs []AgentSpec) {
	d.mu.Lock()
	d.Reviewers = specs
	d.mu.Unlock()
}

func (d *Deps) implementer() AgentSpec {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.Implementer
}

func (d *Deps) reviewerSpecs() []AgentSpec {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]AgentSpec(nil), d.Reviewers...)
}

// CoordinatorSystem returns the coordinator's system prompt for an interaction level.
func CoordinatorSystem(level string) string {
	return coordinatorSystem + "\n\n" + levelGuidance(level)
}

// CoordinatorTools returns the coordinator's tool registry. The coordinator gets
// the Editing set (Read/Write/Edit/Bash) so it can inspect the workspace and review
// diffs first-hand — and could make a tiny touch-up — but the prompt steers it to
// delegate real implementation to the implementer subagent.
func CoordinatorTools(d *Deps, ws *tools.Workspace) *tools.Registry {
	reg := tools.New()
	reg.Add(tools.Editing(ws)...)
	reg.Add(
		listBacklog(d), getTask(d), proposePlan(d),
		spawnImplementer(d), spawnReviewers(d),
		sendToImplementer(d), reReview(d),
		askUser(d), commitTool(d), updateTask(d), createTask(d), tools.Finish(),
	)
	return reg
}

func (d *Deps) newLoop(spec AgentSpec, system string, reg *tools.Registry, actor string) *engine.Loop {
	return &engine.Loop{
		Client:          spec.NewClient(),
		Model:           spec.Model,
		ModelName:       spec.Name,
		Backend:         spec.Backend,
		System:          system,
		Tools:           reg,
		Emitter:         d.Emitter.With(actor),
		MaxTok:          d.MaxTok,
		MaxTurns:        d.MaxTurns,
		Thinking:        spec.Thinking,
		Effort:          spec.Effort,
		ThinkingDisplay: spec.ThinkingDisplay,
	}
}

func listBacklog(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "list_backlog",
		Description: "List backlog tasks with id, status, priority, title, and dependencies. Each open todo/blocked " +
			"task is annotated [READY] when all of its dependencies are done, or [blocked by <ids>] otherwise, and a " +
			"trailing summary lists the ids that are ready to start. Completed (done) tasks are hidden unless include_done is true.",
		Params: tools.Obj(map[string]any{"include_done": tools.BoolProp("include completed (done) tasks in the output (default false)")}),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			ts, err := d.Docs.List()
			if err != nil {
				return tools.ErrResult("list_backlog: %v", err), nil
			}
			includeDone := tools.GetBool(params, "include_done", false)
			byID := docs.StatusByID(ts) // built from the full list so deps on hidden done tasks still resolve
			var b strings.Builder
			hidden := 0
			var ready []string
			for _, t := range ts {
				if t.Status == docs.StatusDone && !includeDone {
					hidden++
					continue
				}
				dep := strings.Join(t.DependsOn, ",")
				if dep == "" {
					dep = "-"
				}
				// Readiness only applies to not-yet-started tasks; in_progress/in_review/done are already past the gate.
				mark := ""
				if t.Status == docs.StatusTodo || t.Status == docs.StatusBlocked {
					if blocking := docs.BlockingDeps(t, byID); len(blocking) > 0 {
						mark = "  [blocked by " + strings.Join(blocking, ",") + "]"
					} else {
						mark = "  [READY]"
						ready = append(ready, t.ID)
					}
				}
				fmt.Fprintf(&b, "%s [%s] p%d  %s  (deps: %s)%s\n", t.ID, t.Status, t.Priority, t.Title, dep, mark)
			}
			if b.Len() == 0 {
				if hidden > 0 {
					return tools.OkResult(fmt.Sprintf("(no open tasks; %d done task(s) hidden — pass include_done=true to show them)", hidden)), nil
				}
				return tools.OkResult("(backlog is empty)"), nil
			}
			if len(ready) > 0 {
				fmt.Fprintf(&b, "\nReady to start (all deps done): %s\n", strings.Join(ready, ", "))
			} else {
				fmt.Fprintf(&b, "\n(no tasks are ready to start — open tasks are blocked, in progress, or in review)\n")
			}
			if hidden > 0 {
				fmt.Fprintf(&b, "(%d done task(s) hidden — pass include_done=true to show them)\n", hidden)
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
			"report plus the staged diff. Provide the task id and your plan. Call once per task; use " +
			"send_to_implementer for follow-up revisions.",
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
			// Delegating a task is an unambiguous focus signal (spec §20.2).
			d.emitFocus(id)
			reg := tools.New()
			reg.Add(tools.Worker(&tools.Workspace{Root: d.Workspace})...)
			impl := d.implementer()
			loop := d.newLoop(impl, implementerSystem+"\n\n"+workspaceNote(d.Workspace), reg, "implementer")
			// The implementer needs more output headroom than the shared cap: a
			// single turn may interleave an extended-thinking block with a large
			// multi-file edit, and the thinking counts against the same budget. Too
			// low a cap truncates the turn before any tool call lands (see fix in
			// engine.Run). Floor it so a thorough turn isn't cut off mid-thought.
			if loop.MaxTok < implementerMinTok {
				loop.MaxTok = implementerMinTok
			}
			loop.Seed(implementerPrompt(t, plan))
			d.mu.Lock()
			d.impl = loop
			d.mu.Unlock()

			before, _ := d.Repo.Diff()
			d.Emitter.Emit(event.SubagentSpawned, map[string]any{"role": "implementer", "model": impl.Model})
			res, err := loop.Run(ctx)
			if err != nil {
				d.Emitter.Emit(event.SubagentFinished, map[string]any{"role": "implementer", "error": err.Error()})
				return tools.ErrResult("implementer failed: %v", err), nil
			}
			d.Emitter.Emit(event.SubagentFinished, map[string]any{"role": "implementer"})
			return implementerOutcome(d, id, "implementer report", before, res), nil
		},
	}
}

func sendToImplementer(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "send_to_implementer",
		Description: "Send revision instructions to the EXISTING implementer (it keeps its context from before). " +
			"Use this to address review findings. Returns its report and the updated staged diff.",
		Params: tools.Obj(map[string]any{
			"task_id":      tools.StrProp("task id"),
			"instructions": tools.StrProp("clear, consolidated instructions for what to change"),
		}, "task_id", "instructions"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			id, _ := tools.GetString(params, "task_id")
			instr, _ := tools.GetString(params, "instructions")
			d.mu.Lock()
			loop := d.impl
			d.mu.Unlock()
			if loop == nil {
				return tools.ErrResult("send_to_implementer: no implementer yet; call spawn_implementer first"), nil
			}
			loop.Post(revisePrompt(instr))
			before, _ := d.Repo.Diff()
			d.Emitter.Emit(event.SubagentSpawned, map[string]any{"role": "implementer", "model": d.implementer().Model, "revise": true})
			res, err := loop.Run(ctx)
			if err != nil {
				d.Emitter.Emit(event.SubagentFinished, map[string]any{"role": "implementer", "error": err.Error()})
				return tools.ErrResult("implementer failed: %v", err), nil
			}
			d.Emitter.Emit(event.SubagentFinished, map[string]any{"role": "implementer"})
			return implementerOutcome(d, id, "revision", before, res), nil
		},
	}
}

func spawnReviewers(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "spawn_reviewers",
		Description: "Get independent reviews of the implementer's changes, running concurrently. Match review " +
			"intensity to the change via the optional review_tier: 'simple' (you, the coordinator, review the change " +
			"yourself — NO reviewer agent is spawned; only for tiny, low-risk changes), 'single-opus' (one reviewer; " +
			"the sensible default for ordinary changes), or 'high-powered' (parallel multi-model review when " +
			"configured with multiple models — for large, risky, security-sensitive, or hard-to-reverse changes). " +
			"Omit review_tier to use the configured default. " +
			"Returns each verdict (accept/revise) and findings; the chosen tier is recorded in the work log.",
		Params: tools.Obj(map[string]any{
			"task_id":     tools.StrProp("task id"),
			"review_tier": tools.StrProp("review tier to use (e.g. simple, single-opus, high-powered); default is the configured default"),
		}, "task_id"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			id, _ := tools.GetString(params, "task_id")
			t, err := d.Docs.Get(id)
			if err != nil {
				return tools.ErrResult("spawn_reviewers: %v", err), nil
			}
			tier, _ := tools.GetString(params, "review_tier")
			var plan ReviewPlan
			if d.ReviewTier != nil {
				plan = d.ReviewTier(tier)
			} else {
				plan = ReviewPlan{Tier: "default", Requested: tier, Specs: d.reviewerSpecs()}
			}

			// Surface the tier selection in events and the work log (always).
			revNames := make([]string, 0, len(plan.Specs))
			for _, s := range plan.Specs {
				revNames = append(revNames, s.Name)
			}
			d.Emitter.Emit(event.ReviewTierSelected, map[string]any{
				"task": id, "tier": plan.Tier, "requested": plan.Requested,
				"self_review": plan.SelfReview, "fallback": plan.Fallback, "reviewers": revNames,
			})
			logLine := fmt.Sprintf("review tier: %s", plan.Tier)
			if plan.SelfReview {
				logLine += " (coordinator self-review)"
			} else if len(revNames) > 0 {
				logLine += " — reviewers: " + strings.Join(revNames, ", ")
			}
			if plan.Fallback && plan.Requested != "" {
				logLine += fmt.Sprintf(" [requested %q unknown; used default]", plan.Requested)
			}
			d.Docs.AppendWorkLog(id, logLine)

			if plan.SelfReview {
				d.mu.Lock()
				d.reviewers = nil
				d.mu.Unlock()
				return tools.OkResult("Review tier 'simple': no reviewer agent was spawned — you (the coordinator) " +
					"must review this change yourself. Inspect the diff (run 'git diff'), check it against the task's " +
					"acceptance criteria, and decide whether to commit or send revisions to the implementer."), nil
			}

			specs := plan.Specs
			if len(specs) == 0 {
				specs = d.reviewerSpecs()
			}
			d.mu.Lock()
			d.reviewers = nil
			for _, spec := range specs {
				reg := tools.New()
				reg.Add(tools.Reviewer(&tools.Workspace{Root: d.Workspace})...)
				loop := d.newLoop(spec, reviewerSystem+"\n\n"+workspaceNote(d.Workspace), reg, "reviewer:"+spec.Name)
				loop.Seed(reviewerPrompt(t))
				d.reviewers = append(d.reviewers, &reviewerHandle{name: spec.Name, loop: loop})
			}
			handles := d.reviewers
			d.mu.Unlock()
			results := runReviewers(ctx, d, handles, id)
			return tools.OkResult(aggregateReviews(results)), nil
		},
	}
}

func reReview(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "re_review",
		Description: "Ask the SAME reviewers (keeping their context) to re-review after a revision. Run this after " +
			"send_to_implementer. Returns updated verdicts.",
		Params: tools.Obj(map[string]any{"task_id": tools.StrProp("task id")}, "task_id"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			id, _ := tools.GetString(params, "task_id")
			d.mu.Lock()
			handles := d.reviewers
			d.mu.Unlock()
			if len(handles) == 0 {
				return tools.ErrResult("re_review: no reviewers yet; call spawn_reviewers first"), nil
			}
			for _, h := range handles {
				h.loop.Post(reReviewPrompt)
			}
			results := runReviewers(ctx, d, handles, id)
			return tools.OkResult(aggregateReviews(results)), nil
		},
	}
}

func askUser(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "ask_user",
		Description: "Ask the user one or more questions and get their answers. Use only as your interaction level " +
			"permits. In autonomous mode no human answers; you will be told to proceed on your own judgement. " +
			"For a single question, pass `question` (and optional `options`, a short list of suggested answers). " +
			"To ask several questions in one round-trip, pass `questions`: a list where each item has its own " +
			"`question` text and its own optional `options` list. The client renders options as a picker so the " +
			"user can choose crisply, and may still type free text. Answers are returned mapped to each question.",
		Params: tools.Obj(map[string]any{
			"question": tools.StrProp("the question for the user (single-question form)"),
			"options":  tools.StrArrProp("optional suggested answers to offer as selectable choices (single-question form)"),
			"questions": map[string]any{
				"type":        "array",
				"description": "ask several questions at once; each item has its own question text and options",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"question": tools.StrProp("the question for the user"),
						"options":  tools.StrArrProp("optional suggested answers to offer as selectable choices"),
					},
					"required": []string{"question"},
				},
			},
		}),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			rawQs := tools.GetMapSlice(params, "questions")
			if len(rawQs) > 0 {
				var qs []Question
				for _, qm := range rawQs {
					prompt, _ := tools.GetString(qm, "question")
					if strings.TrimSpace(prompt) == "" {
						continue
					}
					qs = append(qs, Question{Prompt: prompt, Options: tools.GetStringSlice(qm, "options")})
				}
				if len(qs) == 0 {
					return tools.ErrResult("ask_user: 'questions' must contain at least one question with non-empty text"), nil
				}
				ans, err := d.Asker.AskMany(ctx, qs)
				if err != nil {
					return tools.ErrResult("ask_user: %v", err), nil
				}
				var b strings.Builder
				for i, q := range qs {
					if i > 0 {
						b.WriteString("\n\n")
					}
					a := ""
					if i < len(ans) {
						a = ans[i]
					}
					fmt.Fprintf(&b, "Q%d: %s\nA%d: %s", i+1, q.Prompt, i+1, a)
				}
				return tools.OkResult(b.String()), nil
			}

			q, _ := tools.GetString(params, "question")
			if strings.TrimSpace(q) == "" {
				return tools.ErrResult("ask_user: provide a 'question' or a non-empty 'questions' list"), nil
			}
			opts := tools.GetStringSlice(params, "options")
			ans, err := d.Asker.Ask(ctx, q, opts)
			if err != nil {
				return tools.ErrResult("ask_user: %v", err), nil
			}
			return tools.OkResult(ans), nil
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
			// Moving a task in_progress is the coordinator accepting it: record the
			// session→task focus for cost attribution (spec §20.2).
			if status == "in_progress" {
				d.emitFocus(id)
			}
			d.Docs.RenderIndex()
			d.Emitter.Emit(event.DocUpdated, map[string]any{"task": id, "status": status})
			return tools.OkResult(fmt.Sprintf("task %s -> %s", id, status)), nil
		},
	}
}

// runReviewers runs each reviewer's loop concurrently and waits for all (barrier),
// emitting events and recording each verdict in the work log.
func runReviewers(ctx context.Context, d *Deps, handles []*reviewerHandle, taskID string) []reviewResult {
	results := make([]reviewResult, len(handles))
	var wg sync.WaitGroup
	for i, h := range handles {
		wg.Add(1)
		go func(i int, h *reviewerHandle) {
			defer wg.Done()
			d.Emitter.Emit(event.SubagentSpawned, map[string]any{"role": "reviewer", "model": h.name})
			res, err := h.loop.Run(ctx)
			rv := review{Verdict: "unknown"}
			if err != nil {
				rv.Summary = "reviewer error: " + err.Error()
			} else {
				rv = parseReview(res.Report)
			}
			d.Emitter.Emit(event.ReviewSubmitted, map[string]any{
				"model": h.name, "verdict": rv.Verdict, "summary": rv.Summary, "findings": len(rv.Findings),
			})
			d.Emitter.Emit(event.SubagentFinished, map[string]any{"role": "reviewer", "model": h.name})
			results[i] = reviewResult{name: h.name, rv: rv}
		}(i, h)
	}
	wg.Wait()
	for _, r := range results {
		d.Docs.AppendWorkLog(taskID, fmt.Sprintf("review (%s): %s — %s", r.name, r.rv.Verdict, oneLine(r.rv.Summary)))
	}
	return results
}

// implementerOutcome turns a finished implementer Run into the result the
// coordinator sees. It guards the puzzling no-op case — an empty report with no
// new changes since `before` (the diff captured just before the run) — by
// returning an actionable error instead of a blank report, so the coordinator
// retries with a tighter plan rather than wondering why nothing happened. The
// most common cause is a turn cut off at the token cap before any tool call
// (res.Truncated); the engine already turns that into a Run error, and this is
// the backstop for any other way the implementer yields without doing work.
func implementerOutcome(d *Deps, id, label, before string, res *engine.Result) *gollama.ToolResult {
	after, _ := d.Repo.Diff()
	if strings.TrimSpace(res.Report) == "" && strings.TrimSpace(after) == strings.TrimSpace(before) {
		d.Docs.AppendWorkLog(id, label+": no progress (empty report, no new changes)")
		msg := "implementer returned no report and made no changes to the workspace."
		if res.Truncated {
			msg += " Its turn was cut off at the output token limit before it could act."
		}
		msg += " Re-spawn it with a tighter, concrete plan (name the exact files and edits) or split the task into smaller steps."
		return tools.ErrResult("%s", msg)
	}
	d.Docs.AppendWorkLog(id, label+": "+oneLine(res.Report))
	return tools.OkResult(reportWithDiff(d, res.Report))
}

func reportWithDiff(d *Deps, report string) string {
	diff, _ := d.Repo.Diff()
	out := "IMPLEMENTER REPORT:\n" + report + "\n\n=== STAGED DIFF ===\n" + truncate(diff, maxDiffChars)
	if strings.TrimSpace(diff) == "" {
		out += "(no changes in the workspace)"
	}
	return out
}

// --- review parsing & aggregation ---

type finding struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type review struct {
	Verdict  string    `json:"verdict"`
	Summary  string    `json:"summary"`
	Findings []finding `json:"findings"`
}

type reviewResult struct {
	name string
	rv   review
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

func aggregateReviews(results []reviewResult) string {
	accepts := 0
	for _, r := range results {
		if r.rv.Verdict == "accept" {
			accepts++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "REVIEW SUMMARY: %d/%d reviewers accept\n\n", accepts, len(results))
	for _, r := range results {
		fmt.Fprintf(&b, "--- %s: %s ---\n%s\n", r.name, r.rv.Verdict, r.rv.Summary)
		for _, f := range r.rv.Findings {
			fmt.Fprintf(&b, "  - [%s] %s\n", f.Severity, f.Message)
		}
	}
	if accepts == len(results) {
		b.WriteString("\nAll reviewers accept — you may commit.")
	} else {
		b.WriteString("\nNot all reviewers accept — consolidate the findings and send_to_implementer, then re_review.")
	}
	return b.String()
}

func renderTask(t *docs.Task) string {
	return fmt.Sprintf("id: %s\ntitle: %s\nstatus: %s\npriority: %d\ndepends_on: %s\nspec_refs: %s\n\n%s",
		t.ID, t.Title, t.Status, t.Priority, strings.Join(t.DependsOn, ","), strings.Join(t.SpecRefs, ","), t.Body)
}

func oneLine(s string) string {
	return truncate(strings.TrimSpace(strings.ReplaceAll(s, "\n", " ")), 200)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…[truncated]"
}
