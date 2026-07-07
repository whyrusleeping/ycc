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
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/sandbox"
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
	// Retry is the loop-level transient-failure retry policy applied to subagent
	// loops (implementer/reviewers). Zero value => engine default (task 0133).
	Retry engine.RetryPolicy
	// ReviewTier resolves a requested review tier name (possibly empty) into a
	// concrete ReviewPlan — which reviewer agents to spawn, or that the
	// coordinator self-reviews (spec §13). Nil-safe: when unset, spawn_reviewers
	// falls back to the configured reviewer fan-out (current default behaviour).
	ReviewTier func(name string) ReviewPlan

	// WriteRoots are configured extra writable roots outside the workspace,
	// passed through to tool workspaces so Write/Edit can target them (e.g.
	// sibling projects). Reads are unrestricted; writes default to the
	// workspace plus these roots.
	WriteRoots []string

	// Jobs is the session-scoped background-job registry (docs/design/async-jobs.md).
	// When set it enables background bash (Bash run_in_background) and the
	// job_output/wait/kill_job tools; the session kills all jobs on end.
	Jobs *jobs.Registry

	mu        sync.Mutex
	impl      *engine.Loop
	implJob   *jobs.Job // live/last background implementer job (nil if last spawn was foreground)
	reviewers []*reviewerHandle
	reviewJob *jobs.Job // live/last background reviewers job
	focus     string    // backlog task currently in focus (spec §20.2); guarded by mu
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

// (The coordinator's system prompt is assembled by BuildMode via sys() in
// modes.go, the single assembly path shared by every agent role.)

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
		askUser(d), commitTool(d), updateTask(d), createTask(d), remember(d), tools.Finish(),
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
		Retry:           d.Retry,
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
			"trailing summary lists the ids that are ready to start. 'proposed' tasks are ideas awaiting the user's " +
			"acceptance — never ready to start. Completed (done) tasks are hidden unless include_done is true.",
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
			proposed := 0
			var ready []string
			for _, t := range ts {
				if t.Status == docs.StatusDone && !includeDone {
					hidden++
					continue
				}
				if t.Status == docs.StatusProposed {
					proposed++
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
			if proposed > 0 {
				fmt.Fprintf(&b, "(%d proposed task(s) — ideas awaiting the user's acceptance; promote to 'todo' with update_task only when the user confirms)\n", proposed)
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
		Name: "propose_plan",
		Description: "Record your implementation plan for a task before delegating. Call once after choosing a task. " +
			"Optionally attach concise, advisory context_hints (relevant file paths, function/symbol refs, or small " +
			"snippets) recorded alongside the plan as non-prescriptive starting points for the implementer.",
		Params: tools.Obj(map[string]any{
			"task_id":       tools.StrProp("task id"),
			"plan":          tools.StrProp("the implementation plan"),
			"context_hints": tools.StrArrProp("optional, concise advisory starting points — relevant file paths, function/symbol refs, or small snippets — recorded alongside the plan as non-prescriptive hints to cut the implementer's redundant exploration; keep them short, no full-file dumps"),
		}, "task_id", "plan"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			id, _ := tools.GetString(params, "task_id")
			plan, _ := tools.GetString(params, "plan")
			hints := boundHints(tools.GetStringSlice(params, "context_hints"))
			d.Emitter.Emit(event.PlanProposed, map[string]any{"task": id, "plan": plan})
			// Persist the FULL plan to the task's "## Plan" section (durable,
			// human-browsable), and keep a dated one-line work-log breadcrumb (task 0020).
			// When the coordinator supplied context hints, append them as a "### Starting
			// points" subsection so the durable plan artifact records them too (task 0079).
			planDoc := plan
			if len(hints) > 0 {
				planDoc += "\n\n### Starting points\n"
				for _, h := range hints {
					planDoc += "- " + h + "\n"
				}
			}
			if _, err := d.Docs.SetPlan(id, planDoc); err != nil {
				return tools.ErrResult("propose_plan: %v", err), nil
			}
			if _, err := d.Docs.AppendWorkLog(id, "plan: "+oneLine(plan)); err != nil {
				return tools.ErrResult("propose_plan: %v", err), nil
			}
			if len(hints) > 0 {
				d.Docs.AppendWorkLog(id, fmt.Sprintf("context hints: %d recorded with plan", len(hints)))
			}
			return tools.OkResult("plan recorded"), nil
		},
	}
}

// (The former list_plans/run_plan/save_plan tools were removed: plans are plain
// committed markdown in plans/*.md, so agents browse them with Read/Bash and save
// them with Write — the plan-format convention lives in the prompts and spec §6.3.
// The docs package keeps ListPlans/ReadPlan/SavePlan for the TUI/RPC browsing surface.)

func spawnImplementer(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "spawn_implementer",
		Description: "Delegate implementation of a task to a coding subagent. It edits the workspace and returns a " +
			"report plus the staged diff. Provide the task id and your plan. Optionally attach concise, advisory " +
			"context_hints (relevant file paths, function/symbol refs, or small snippets) surfaced to the worker as " +
			"non-prescriptive 'starting points'. Call once per task; use send_to_implementer for follow-up revisions. " +
			"Pass background:true to run it as a background job (returns a job_id immediately, report arrives via wait " +
			"or automatically) — only when you have genuinely independent work to do meanwhile; at most one mutating " +
			"job per tree.",
		Params: tools.Obj(map[string]any{
			"task_id":       tools.StrProp("task id"),
			"plan":          tools.StrProp("the plan the implementer should follow"),
			"context_hints": tools.StrArrProp("optional, concise advisory starting points — relevant file paths, function/symbol refs, or small snippets — surfaced to the worker as non-prescriptive hints to cut redundant exploration; keep them short, no full-file dumps"),
			"background":    tools.BoolProp("run as a background job: return a job_id immediately instead of blocking; its report arrives automatically or via wait. Use only for genuinely independent work — refused while another mutating job is live in this tree (route parallel mutating work through a workstream)"),
		}, "task_id", "plan"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			id, _ := tools.GetString(params, "task_id")
			plan, _ := tools.GetString(params, "plan")
			background := tools.GetBool(params, "background", false)
			hints := boundHints(tools.GetStringSlice(params, "context_hints"))
			t, err := d.Docs.Get(id)
			if err != nil {
				return tools.ErrResult("spawn_implementer: %v", err), nil
			}
			// Single-writer guard (design §3.4). A BACKGROUND spawn is refused while
			// ANY mutating job (implementer or mutating background bash) is live in
			// this tree. A FOREGROUND spawn is refused only while another mutating
			// AGENT job (a background implementer) is live — two implementers can
			// never share a tree — but runs fine alongside a background bash job, as
			// it does today.
			if background {
				if d.Jobs == nil {
					return tools.ErrResult("spawn_implementer: background subagents are not available in this session"), nil
				}
				if live := d.Jobs.LiveMutating(); live != nil {
					return tools.ErrResult("spawn_implementer: another mutating job (%s: %s) is live in this tree; wait for it or kill_job it, or route parallel mutating work through a separate workstream (spec §14.1)", live.ID(), live.Label()), nil
				}
			} else if live := d.liveImplJob(); live != nil {
				return tools.ErrResult("spawn_implementer: a background implementer (%s: %s) is still running in this tree; wait for it or kill_job it before spawning another implementer, or route parallel mutating work through a separate workstream (spec §14.1)", live.ID(), live.Label()), nil
			}
			// Delegating a task is an unambiguous focus signal (spec §20.2).
			d.emitFocus(id)
			// Record a best-effort work-log breadcrumb noting the advisory hints
			// surfaced to the worker (task 0079); don't fail the spawn on a write error.
			if len(hints) > 0 {
				d.Docs.AppendWorkLog(id, "context hints: "+oneLine(strings.Join(hints, "; ")))
			}
			reg := tools.New()
			reg.Add(tools.Worker(&tools.Workspace{
				Root:       d.Workspace,
				WriteRoots: tools.NormalizeRoots(d.WriteRoots),
				Jobs:       d.Jobs,
				Emitter:    d.Emitter.With("implementer"),
			})...)
			impl := d.implementer()
			loop := d.newLoop(impl, sys(implementerSystem, "", d.Workspace), reg, "implementer")
			// The implementer needs more output headroom than the shared cap: a
			// single turn may interleave an extended-thinking block with a large
			// multi-file edit, and the thinking counts against the same budget. Too
			// low a cap truncates the turn before any tool call lands (see fix in
			// engine.Run). Floor it so a thorough turn isn't cut off mid-thought.
			if loop.MaxTok < implementerMinTok {
				loop.MaxTok = implementerMinTok
			}
			loop.Seed(implementerPrompt(t, plan, hints))
			d.mu.Lock()
			d.impl = loop
			d.implJob = nil // cleared for a foreground spawn; set below for background
			d.mu.Unlock()

			before, _ := d.Repo.Diff()

			if background {
				// Register a mutating agent job and run the child loop under its
				// context (so kill_job / session-end KillAll cancel it). The final
				// report is the SAME text the synchronous path returns, delivered
				// exactly once via wait or checkpoint injection.
				job := d.Jobs.StartMutating("agent", "implementer "+id, d.Emitter.Actor())
				d.mu.Lock()
				d.implJob = job
				d.mu.Unlock()
				d.Emitter.Emit(event.JobStarted, map[string]any{"id": job.ID(), "kind": job.Kind(), "label": job.Label()})
				d.Emitter.Emit(event.SubagentSpawned, map[string]any{"role": "implementer", "model": impl.Model, "job_id": job.ID()})
				go func() {
					out := runImplementer(job.Context(), d, loop, id, "implementer report", before, job.ID())
					status := jobs.Done
					if out.IsError {
						status = jobs.Failed
					}
					if job.Finish(status, out.Content) {
						emitAgentJobFinished(d.Emitter, job)
					}
				}()
				return tools.OkResult(fmt.Sprintf("started background job %s: implementer on task %s. "+
					"It runs in the background — do NOT poll it. Its report arrives automatically when it "+
					"finishes, or call wait([%q]) when its result gates your next step.", job.ID(), id, job.ID())), nil
			}

			d.Emitter.Emit(event.SubagentSpawned, map[string]any{"role": "implementer", "model": impl.Model})
			return runImplementer(ctx, d, loop, id, "implementer report", before, ""), nil
		},
	}
}

// liveImplJob returns the background implementer job if one is still running,
// else nil. The single-writer guard uses it to refuse a second implementer in the
// same tree (foreground or background).
func (d *Deps) liveImplJob() *jobs.Job {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.implJob != nil && d.implJob.Status() == jobs.Running {
		return d.implJob
	}
	return nil
}

// runImplementer runs an implementer loop to completion, emits subagent_finished
// (tagged with jobID when the run is a background job), and returns the
// coordinator-facing outcome — identical whether the loop runs synchronously or in
// a background goroutine, so both delivery paths carry the same report text.
func runImplementer(ctx context.Context, d *Deps, loop *engine.Loop, id, label, before, jobID string) *gollama.ToolResult {
	res, err := loop.Run(ctx)
	fin := map[string]any{"role": "implementer"}
	if jobID != "" {
		fin["job_id"] = jobID
	}
	if err != nil {
		fin["error"] = err.Error()
		d.Emitter.Emit(event.SubagentFinished, fin)
		return tools.ErrResult("implementer failed: %v", err)
	}
	if res.Blocked {
		fin["blocked"] = true
	}
	d.Emitter.Emit(event.SubagentFinished, fin)
	return implementerOutcome(d, id, label, before, res)
}

// emitAgentJobFinished records a job_finished event for an agent job, tagged with
// the coordinator actor and carrying its final status and report tail. Mirrors
// tools.emitJobFinished for the bash-job path.
func emitAgentJobFinished(em *event.Emitter, job *jobs.Job) {
	rep := job.Report()
	em.Emit(event.JobFinished, map[string]any{
		"id": rep.ID, "kind": rep.Kind, "label": rep.Label,
		"status": string(rep.Status), "tail": rep.Result,
	})
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
			job := d.implJob
			d.mu.Unlock()
			if loop == nil {
				return tools.ErrResult("send_to_implementer: no implementer yet; call spawn_implementer first"), nil
			}
			// A background implementer's loop is only addressable once it has
			// finished — resuming a still-running loop would run two turns of the
			// same conversation concurrently.
			if job != nil && job.Status() == jobs.Running {
				return tools.ErrResult("send_to_implementer: implementer job %s is still running; wait for its report first", job.ID()), nil
			}
			loop.Post(revisePrompt(instr))
			before, _ := d.Repo.Diff()
			d.Emitter.Emit(event.SubagentSpawned, map[string]any{"role": "implementer", "model": d.implementer().Model, "revise": true})
			res, err := loop.Run(ctx)
			if err != nil {
				d.Emitter.Emit(event.SubagentFinished, map[string]any{"role": "implementer", "error": err.Error()})
				return tools.ErrResult("implementer failed: %v", err), nil
			}
			if res.Blocked {
				d.Emitter.Emit(event.SubagentFinished, map[string]any{"role": "implementer", "blocked": true})
			} else {
				d.Emitter.Emit(event.SubagentFinished, map[string]any{"role": "implementer"})
			}
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
			"Returns each verdict (accept/revise) and findings; the chosen tier is recorded in the work log. " +
			"Pass background:true to run the review set as a background job (returns a job_id immediately; verdicts " +
			"arrive via wait or automatically). Reviewers are read-only and run freely in parallel with other work.",
		Params: tools.Obj(map[string]any{
			"task_id":     tools.StrProp("task id"),
			"review_tier": tools.StrProp("review tier to use (e.g. simple, single-opus, high-powered); default is the configured default"),
			"background":  tools.BoolProp("run the reviewers as a background job: return a job_id immediately instead of blocking; verdicts arrive automatically or via wait. Reviewers are read-only so this is always allowed"),
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
			// Reviewer Bash is sandboxed read-only where the host supports it; warn
			// once per spawn when it isn't so operators know reviewer non-mutation is
			// only prompt-enforced on this platform.
			if sandbox.Available() == sandbox.None {
				d.Emitter.Emit(event.Narration, map[string]any{
					"msg": "reviewer bash sandbox unavailable on this platform; reviewer non-mutation is prompt-enforced only",
				})
			}
			d.mu.Lock()
			d.reviewers = nil
			for _, spec := range specs {
				reg := tools.New()
				reg.Add(tools.Reviewer(&tools.Workspace{Root: d.Workspace})...)
				loop := d.newLoop(spec, inspectSys(reviewerSystem, d.Workspace), reg, "reviewer:"+spec.Name)
				loop.Seed(reviewerPrompt(t))
				d.reviewers = append(d.reviewers, &reviewerHandle{name: spec.Name, loop: loop})
			}
			handles := d.reviewers
			d.reviewJob = nil // cleared for a foreground run; set below for background
			d.mu.Unlock()

			if tools.GetBool(params, "background", false) {
				if d.Jobs == nil {
					return tools.ErrResult("spawn_reviewers: background subagents are not available in this session"), nil
				}
				// Reviewers are read-only, so a reviewer job is non-mutating and runs
				// freely in parallel with anything (no single-writer guard).
				job := d.Jobs.Start("agent", "reviewers "+id, d.Emitter.Actor())
				d.mu.Lock()
				d.reviewJob = job
				d.mu.Unlock()
				d.Emitter.Emit(event.JobStarted, map[string]any{"id": job.ID(), "kind": job.Kind(), "label": job.Label()})
				go func() {
					results := runReviewers(job.Context(), d, handles, id)
					if job.Finish(jobs.Done, aggregateReviews(results)) {
						emitAgentJobFinished(d.Emitter, job)
					}
				}()
				return tools.OkResult(fmt.Sprintf("started background job %s: reviewers on task %s. "+
					"Their verdicts arrive automatically when the review finishes, or call wait([%q]).", job.ID(), id, job.ID())), nil
			}

			results := runReviewers(ctx, d, handles, id)
			return tools.OkResultView(aggregateReviews(results), aggregateReviewsView(results)), nil
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
			job := d.reviewJob
			d.mu.Unlock()
			if len(handles) == 0 {
				return tools.ErrResult("re_review: no reviewers yet; call spawn_reviewers first"), nil
			}
			if job != nil && job.Status() == jobs.Running {
				return tools.ErrResult("re_review: reviewers job %s is still running; wait for its verdicts first", job.ID()), nil
			}
			for _, h := range handles {
				h.loop.Post(reReviewPrompt)
			}
			results := runReviewers(ctx, d, handles, id)
			return tools.OkResultView(aggregateReviews(results), aggregateReviewsView(results)), nil
		},
	}
}

func askUser(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "ask_user",
		Description: "Ask the user one or more questions and get their answers. Use only as your interaction level " +
			"permits. In autonomous mode no human answers; you will be told to proceed on your own judgement. " +
			"Make each question SELF-CONTAINED: the user has not been following your work, so briefly give the " +
			"context needed to answer well — what you were doing, what you found or tried, and why you're asking " +
			"(one to three sentences before the question itself). Don't assume they can see your transcript. " +
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
			// Record the acceptance decision in the work log BEFORE committing so the
			// final backlog state (status, work log) is captured in the same commit
			// (Commit does `git add -A`), leaving no leftover uncommitted backlog files.
			// The sha is not known until after the commit and embedding it would change
			// the tree, so the work-log line omits it.
			d.Docs.AppendWorkLog(id, "decision: accept — commit: "+oneLine(msg))
			sha, err := d.Repo.Commit(msg)
			if err != nil {
				return tools.ErrResult("commit: %v", err), nil
			}
			d.Emitter.Emit(event.DecisionMade, map[string]any{"task": id, "decision": "accept"})
			d.Emitter.Emit(event.CommitMade, map[string]any{"task": id, "sha": sha, "message": msg})
			return tools.OkResult("committed " + sha), nil
		},
	}
}

func updateTask(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name:        "update_task",
		Description: "Update a task's status (proposed, todo, in_progress, in_review, done, blocked). Promoting a 'proposed' task to 'todo' marks it accepted by the user.",
		Params: tools.Obj(map[string]any{
			"task_id": tools.StrProp("task id"),
			"status":  map[string]any{"type": "string", "enum": []string{"proposed", "todo", "in_progress", "in_review", "done", "blocked"}, "description": "new status"},
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
// coordinator sees. It guards the puzzling no-op case — no new changes since
// `before` (the diff captured just before the run) combined with either an empty
// report or a degenerate no-content yield (res.NoContent: the model produced
// neither a tool call nor any real content, so res.Report holds only a
// synthesized stop-reason note). It returns an actionable error instead of a
// blank/placeholder report, so the coordinator retries with a tighter plan
// rather than wondering why nothing happened. The most common cause is a turn
// cut off at the token cap before any tool call (res.Truncated); the engine
// already turns that into a Run error, and this is the backstop for any other
// way the implementer yields without doing work.
func implementerOutcome(d *Deps, id, label, before string, res *engine.Result) *gollama.ToolResult {
	// A structured blocked escalation is handled FIRST — before the no-progress
	// guard, which must not fire for a legitimate blocked report even when there
	// are no workspace changes. The reason lands in the work log; the coordinator
	// is told to resolve it, escalate, or mark the task blocked rather than push
	// the implementer to guess.
	if res.Blocked {
		reason := strings.TrimSpace(res.Report)
		if reason == "" {
			reason = "(no reason given)"
		}
		d.Docs.AppendWorkLog(id, label+": BLOCKED — "+oneLine(reason))
		diff, _ := d.Repo.Diff()
		out := "IMPLEMENTER BLOCKED (not finished): it cannot proceed without a decision.\n\nREASON: " + reason +
			"\n\n=== STAGED DIFF (partial work may exist) ===\n" + truncate(diff, maxDiffChars)
		if strings.TrimSpace(diff) == "" {
			out += "(no changes in the workspace)"
		}
		out += "\n\nDo not push it to guess. If this is an ordinary judgement call, decide it yourself and " +
			"send_to_implementer with the answer (it keeps its context). If the user is genuinely needed, ask_user as " +
			"your interaction level permits and relay the answer via send_to_implementer. If no answer is available, " +
			"update_task 'blocked' with the reason (already recorded in the work log)."
		return tools.OkResult(out)
	}
	after, _ := d.Repo.Diff()
	noReport := strings.TrimSpace(res.Report) == "" || res.NoContent
	if noReport && strings.TrimSpace(after) == strings.TrimSpace(before) {
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

// aggregateReviewsView builds the structured tree the TUI renders for a review
// round: a "N/M reviewers accept" headline, one node per reviewer (verdict as
// detail), and findings nested beneath, severity-colored. It mirrors the textual
// aggregateReviews summary the model reads.
func aggregateReviewsView(results []reviewResult) *tools.ResultView {
	accepts := 0
	for _, r := range results {
		if r.rv.Verdict == "accept" {
			accepts++
		}
	}
	status := "ok"
	if accepts < len(results) {
		status = "warn"
	}
	v := &tools.ResultView{
		Summary: fmt.Sprintf("%d/%d reviewers accept", accepts, len(results)),
		Status:  status,
	}
	for _, r := range results {
		kind := "ok"
		switch r.rv.Verdict {
		case "accept":
			kind = "ok"
		case "reject":
			kind = "error"
		default:
			kind = "warn"
		}
		node := tools.ViewNode{Label: r.name, Detail: r.rv.Verdict, Kind: kind}
		if s := strings.TrimSpace(r.rv.Summary); s != "" {
			node.Children = append(node.Children, tools.ViewNode{Label: oneLine(s), Kind: "muted"})
		}
		for _, f := range r.rv.Findings {
			fk := "muted"
			switch f.Severity {
			case "blocker", "major", "critical":
				fk = "error"
			case "minor", "nit", "suggestion":
				fk = "warn"
			}
			node.Children = append(node.Children, tools.ViewNode{Label: f.Message, Detail: "[" + f.Severity + "]", Kind: fk})
		}
		v.Nodes = append(v.Nodes, node)
	}
	return v
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
