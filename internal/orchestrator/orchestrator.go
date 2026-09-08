// Package orchestrator builds coordinator, implementer, and reviewer agents and
// their tools. It manages task focus, delegation, review, revision, and commit.
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

const maxRevisionHandoffBytes = 32 * 1024

// AgentSpec describes how to build a subagent's backend.
type AgentSpec struct {
	Name      string // logical model name (used for usage attribution / pricing)
	NewClient func() engine.Turner
	Model     string
	Backend   string // logical backend family (e.g. "anthropic"); labels usage events
	// Label distinguishes reviewer roles that may use the same model.
	Label string
	// Focus is optional role guidance appended to the reviewer's system prompt.
	Focus string
	// Thinking fields carry the model's reasoning settings; zero disables them.
	Thinking        string
	Effort          string
	ThinkingDisplay string
	// ContextWindow and ContextSafeFraction budget retained input context. They
	// are independent of the per-turn output cap in Deps.MaxTok.
	ContextWindow       int
	ContextSafeFraction float64
}

// label returns the agent's display/actor label, defaulting to its model name.
func (s AgentSpec) label() string {
	if strings.TrimSpace(s.Label) != "" {
		return s.Label
	}
	return s.Name
}

// Question is one prompt in a batch ask_user call, with its own optional set of
// suggested answers.
type Question struct {
	Prompt  string
	Options []string
}

// Asker lets the coordinator ask the user a question. Implemented by the session.
type Asker interface {
	Ask(ctx context.Context, question string, options []string) (string, error)
	// AskMany poses several questions in a single round-trip, each with its own
	// optional set of suggested answers. The returned slice is parallel to the
	// input: answers[i] is the answer to questions[i]. Internal unattended runs
	// auto-answer rather than waiting with no client attached.
	AskMany(ctx context.Context, questions []Question) ([]string, error)
	// Confirm asks the user a yes/no question for a high-impact, hard-to-reverse
	// action (e.g. starting the work pipeline). It requires a real human answer;
	// when none is available it returns (false, nil), declining safely.
	Confirm(ctx context.Context, question string) (bool, error)
}

// ReviewPlan is the resolved review approach for one spawn_reviewers call: which
// reviewer agents to spawn, or (SelfReview) that the coordinator reviews the
// change itself (the 'self-review' tier). It is produced by Deps.ReviewTier.
type ReviewPlan struct {
	Tier       string      // effective tier name used
	SelfReview bool        // self-review tier: coordinator self-reviews; no agents spawned
	Specs      []AgentSpec // reviewer agents to spawn (empty when SelfReview)
	Requested  string      // tier the coordinator requested (for auditing)
	Fallback   bool        // requested tier was unknown; degraded to default
}

// ReviewTierInfo describes a review tier exposed by spawn_reviewers.
type ReviewTierInfo struct {
	Name        string
	Description string   // "when to pick me" guidance
	Default     bool     // this is the configured default tier
	SelfReview  bool     // coordinator self-reviews; no reviewer agent
	Reviewers   []string // human-readable reviewer line-up ("readability (claude)", …)
}

// Deps is everything the coordinator tools need to orchestrate a work session.
// It also holds the live subagent handles and their resolved slots so revision
// rounds can either retain conversation context or replace it deliberately.
type Deps struct {
	Workspace string
	// Env contains extra KEY=VALUE entries inherited by every agent shell in
	// this session (used by per-project worktree bootstrap configuration).
	Env         []string
	Docs        *docs.Store
	Repo        *git.Repo
	Emitter     *event.Emitter // coordinator emitter (actor "coordinator")
	Implementer AgentSpec
	Reviewers   []AgentSpec
	Asker       Asker
	MaxTok      int
	MaxTurns    int // per-Run tool-call turn cap; 0 => engine default backstop
	// Retry is the subagent retry policy; zero uses the engine default.
	Retry engine.RetryPolicy
	// ReviewTier resolves a tier name to reviewer agents or coordinator self-review.
	// When nil, spawn_reviewers uses the configured reviewer fan-out.
	ReviewTier func(name string) ReviewPlan
	// ResolveAgent resolves any configured logical model for a generic chat
	// subagent. It is session-owned so live thinking overrides are honored.
	ResolveAgent func(name string) (AgentSpec, error)
	// AgentModels lists configured logical model names for the generic spawn tool.
	AgentModels func() []string
	// ReviewTiers lists the review tiers available in this project so the
	// spawn_reviewers tool description can name them (custom tiers included).
	// Nil-safe: when unset the description falls back to the built-in blurb.
	ReviewTiers func() []ReviewTierInfo

	// WriteRoots are configured extra writable roots outside the workspace,
	// passed through to tool workspaces so Write/Edit can target them (e.g.
	// sibling projects). Reads are unrestricted; writes default to the
	// workspace plus these roots.
	WriteRoots []string

	// WorkImplementation is "delegate" (the default) or "direct". Direct mode
	// removes the implementer tools and lets the coordinator edit.
	WorkImplementation string

	// Jobs is the session-scoped background-job registry (docs/design/async-jobs.md).
	// When set it enables background bash (Bash run_in_background) and the
	// job_output/wait/kill_job tools; the session kills all jobs on end.
	Jobs *jobs.Registry

	mu           sync.Mutex
	impl         *engine.Loop
	implSpec     AgentSpec // resolved slot used by impl; retained across fresh revisions
	implRound    int       // completed/attempted implementer runs, including the initial run
	implJob      *jobs.Job // live/last background implementer job (nil if last spawn was foreground)
	implReport   string    // latest compact implementation/verification report for fresh continuation
	reviewers    []*reviewerHandle
	reviewJob    *jobs.Job // live/last background reviewers job
	genericSeq   int
	genericAgent map[string]*genericAgentHandle // stable agent id -> retained loop
	focus        string                         // backlog task currently in focus; guarded by mu
}

type genericAgentHandle struct {
	id      string
	spec    AgentSpec
	loop    *engine.Loop
	job     *jobs.Job
	round   int
	mutates bool
	running bool // Run may still be unwinding after kill_job marks job killed
}

// emitFocus records a task_focus event when the active task changes. Re-focusing
// the same task is a no-op.
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
	// Best-effort: carry the task's title so UIs can label the focus without a
	// backlog lookup of their own. A failed lookup just omits it.
	data := map[string]any{"task": id}
	if d.Docs != nil {
		if t, err := d.Docs.Get(id); err == nil && strings.TrimSpace(t.Title) != "" {
			data["title"] = strings.TrimSpace(t.Title)
		}
	}
	d.Emitter.Emit(event.TaskFocus, data)
}

type reviewerHandle struct {
	name               string // display label (tier reviewer name, or the model name)
	model              string // logical model backing this reviewer
	spec               AgentSpec
	loop               *engine.Loop
	round              int // current review round, including the initial review
	contextMode        string
	priorContextTokens int
	newContextTokens   int
	rolloverReason     string
	handoff            string
	lastReview         review
}

// SetImplementer changes future spawns; a running implementer keeps its context.
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

// CoordinatorTools builds the work toolset. Direct mode omits worker-agent tools;
// delegated mode keeps them while still allowing coordinator inspection and touch-ups.
func CoordinatorTools(d *Deps, ws *tools.Workspace, direct bool) *tools.Registry {
	reg := tools.New()
	reg.Add(tools.Editing(ws)...)
	reg.Add(listBacklog(d), getTask(d), proposePlan(d), spawnReviewers(d), reReview(d))
	if !direct {
		reg.Add(spawnImplementer(d), sendToImplementer(d))
	}
	reg.Add(askUser(d), commitTool(d), updateTask(d), createTask(d), remember(d), tools.Finish())
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
			ts, err := d.Docs.ListMetadata()
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
		Description: "Persist an implementation plan when a task is complex, ambiguous, or multi-step; routine changes do not need one. " +
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
			// Keep the plan beside the task and include any bounded starting points.
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
			return tools.OkResult("plan recorded"), nil
		},
	}
}

// Plans remain plain Markdown; the docs package exposes them to TUI/RPC clients.

func spawnImplementer(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "spawn_implementer",
		Description: "Delegate implementation of a task to a coding subagent. It edits the workspace and returns a " +
			"report plus the staged diff. Provide the task id and a concise approach. Optionally attach advisory " +
			"context_hints (relevant file paths, function/symbol refs, or small snippets) surfaced to the worker as " +
			"non-prescriptive 'starting points'. For files the worker will certainly need, preload_files accepts " +
			"structured path/offset/limit tuples and pre-reads them into its initial context. Call once per task; use send_to_implementer for follow-up revisions. " +
			"Pass background:true to run it as a background job (returns a job_id immediately, report arrives via wait " +
			"or automatically) — only when you have genuinely independent work to do meanwhile; at most one mutating " +
			"job per tree.",
		Params: tools.Obj(map[string]any{
			"task_id":       tools.StrProp("task id"),
			"plan":          tools.StrProp("the concise approach the implementer should follow"),
			"context_hints": tools.StrArrProp("optional, concise advisory starting points — relevant file paths, function/symbol refs, or small snippets — surfaced to the worker as non-prescriptive hints to cut redundant exploration; keep them short, no full-file dumps"),
			"preload_files": map[string]any{
				"type": "array",
				"description": "optional files to pre-read into the implementer's seed context; at most 16 tuples and 64 KiB total. " +
					"Each tuple is {path, offset?, limit?}; stale paths become visible Read errors rather than failing the spawn",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":   tools.StrProp("file path accepted by Read"),
						"offset": map[string]any{"type": "integer", "description": "optional 1-based start line"},
						"limit":  map[string]any{"type": "integer", "description": "optional line limit; capped at 2000"},
					},
					"required": []string{"path"},
				},
			},
			"background": tools.BoolProp("run as a background job: return a job_id immediately instead of blocking; its report arrives automatically or via wait. Use only for genuinely independent work — refused while another mutating job is live in this tree (route parallel mutating work through a workstream)"),
		}, "task_id", "plan"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			id, _ := tools.GetString(params, "task_id")
			plan, _ := tools.GetString(params, "plan")
			background := tools.GetBool(params, "background", false)
			hints := boundHints(tools.GetStringSlice(params, "context_hints"))
			preloads := parsePreloadFiles(params)
			t, err := d.Docs.Get(id)
			if err != nil {
				return tools.ErrResult("spawn_implementer: %v", err), nil
			}
			// Background workers require an entirely idle tree. A foreground worker
			// may overlap shell work, but never another mutating agent.
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
			// Delegating a task makes it the session's active focus.
			d.emitFocus(id)
			reg := tools.New()
			reg.Add(tools.Worker(&tools.Workspace{
				Root:       d.Workspace,
				Env:        append([]string(nil), d.Env...),
				WriteRoots: tools.NormalizeRoots(d.WriteRoots),
				Jobs:       d.Jobs,
				Emitter:    d.Emitter.With("implementer"),
			})...)
			impl := d.implementer()
			loop := d.newLoop(impl, sys(implementerSystem, false, d.Workspace), reg, "implementer")
			// The implementer needs more output headroom than the shared cap: a
			// single turn may interleave an extended-thinking block with a large
			// multi-file edit, and the thinking counts against the same budget. Too
			// low a cap truncates the turn before any tool call lands (see fix in
			// engine.Run). Floor it so a thorough turn isn't cut off mid-thought.
			if loop.MaxTok < implementerMinTok {
				loop.MaxTok = implementerMinTok
			}
			preloaded := buildPreloadHistory(ctx, reg, preloads)
			if len(preloaded.History) > 0 {
				loop.SetHistory(preloaded.History)
				emitSyntheticPreload(d.Emitter, impl, preloaded)
			}
			loop.Seed(implementerPrompt(t, plan, hints))
			d.mu.Lock()
			d.impl = loop
			d.implSpec = impl
			d.implRound = 1
			d.implReport = ""
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
	contextTokens := loop.ContextTokensEstimate()
	fin := map[string]any{"role": "implementer", "context_mode": "fresh", "round": 1, "context_tokens_est": contextTokens}
	if jobID != "" {
		fin["job_id"] = jobID
	}
	if err != nil {
		fin["error"] = err.Error()
		d.Emitter.Emit(event.SubagentFinished, fin)
		return tools.ErrResult("implementer failed: %v\n\nSUBAGENT CONTEXT: mode=fresh round=1 approx_tokens=%d. If this was a context-length failure, retry with send_to_implementer context_mode='fresh'.", err, contextTokens)
	}
	if res.Blocked {
		fin["blocked"] = true
	}
	d.mu.Lock()
	d.implReport = truncate(res.Report, 4096)
	d.mu.Unlock()
	d.Emitter.Emit(event.SubagentFinished, fin)
	out := implementerOutcome(d, id, label, before, res)
	out.Content += subagentContextNote("fresh", 1, contextTokens, 0)
	return out
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
		Description: "Send consolidated revision instructions to the implementer. context_mode defaults to 'retain', " +
			"which reuses its history for a small, local correction. Near the configured model context budget it " +
			"automatically switches to a fresh replacement. Explicit 'fresh' remains useful for a broad rewrite, material " +
			"approach change, or obsolete history. Fresh replacements preserve the resolved slot and receive the full task, " +
			"current bounded diff, latest verification report, and your compact self-contained handoff.",
		Params: tools.Obj(map[string]any{
			"task_id":      tools.StrProp("task id"),
			"instructions": tools.StrProp("clear, consolidated, self-contained instructions: unresolved findings, current intended approach, and required verification; bounded to 32 KiB"),
			"context_mode": tools.StrProp("optional revision context strategy: 'retain' (default) or 'fresh'"),
		}, "task_id", "instructions"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			id, _ := tools.GetString(params, "task_id")
			instr, _ := tools.GetString(params, "instructions")
			instr = boundedRevisionHandoff(instr)
			requestedMode, err := revisionContextMode(params)
			if err != nil {
				return tools.ErrResult("send_to_implementer: %v", err), nil
			}
			d.mu.Lock()
			loop := d.impl
			spec := d.implSpec
			job := d.implJob
			priorRound := d.implRound
			priorReport := d.implReport
			d.mu.Unlock()
			if loop == nil {
				return tools.ErrResult("send_to_implementer: no implementer yet; call spawn_implementer first"), nil
			}
			// A background implementer's loop is only addressable once it has
			// finished — replacing or resuming a still-running loop would permit two
			// mutating agents in the same tree.
			if job != nil && job.Status() == jobs.Running {
				return tools.ErrResult("send_to_implementer: implementer job %s is still running; wait for its report first", job.ID()), nil
			}

			priorTokens := loop.ContextTokensEstimate()
			rolloverOldTokens := priorTokens
			mode, rolloverReason, newTokens := requestedMode, "", 0
			rollover := requestedMode == "fresh"
			if rollover {
				rolloverReason = "coordinator_fresh"
			} else if exceedsContextBudget(spec, loop.ContextTokensEstimateWith(revisePrompt(instr))) {
				rollover = true
				mode = "fresh"
				rolloverReason = "automatic_pressure"
			}
			if rollover {
				t, getErr := d.Docs.Get(id)
				if getErr != nil {
					return tools.ErrResult("send_to_implementer: %v", getErr), nil
				}
				loop = freshImplementerLoop(d, spec, t, instr, priorReport)
				newTokens = loop.ContextTokensEstimate()
				d.mu.Lock()
				d.impl = loop
				d.mu.Unlock()
			} else {
				loop.Post(revisePrompt(instr))
			}
			round := priorRound + 1
			d.mu.Lock()
			d.implRound = round
			d.mu.Unlock()
			before, _ := d.Repo.Diff()
			spawnData := subagentSpawnData("implementer", spec, mode, round, priorTokens, newTokens, rolloverReason)
			spawnData["revise"] = true
			d.Emitter.Emit(event.SubagentSpawned, spawnData)
			loop.ContextLengthHandled = true

			// A context error on the first provider call has executed no tool and is
			// therefore safe to recover once. Never replay a Run after its history grew:
			// an implementer tool may already have mutated the workspace.
			historyLen := len(loop.History())
			res, runErr := loop.Run(ctx)
			if runErr != nil && engine.IsContextLengthError(runErr) && len(loop.History()) == historyLen && ctx.Err() == nil {
				t, getErr := d.Docs.Get(id)
				if getErr == nil {
					oldTokens := loop.ContextTokensEstimate()
					loop = freshImplementerLoop(d, spec, t, instr, priorReport)
					mode, rolloverReason = "fresh", "context_error_recovery"
					rolloverOldTokens = oldTokens
					newTokens = loop.ContextTokensEstimate()
					d.mu.Lock()
					d.impl = loop
					d.mu.Unlock()
					recoverySpawn := subagentSpawnData("implementer", spec, mode, round, oldTokens, newTokens, rolloverReason)
					recoverySpawn["revise"] = true
					d.Emitter.Emit(event.SubagentSpawned, recoverySpawn)
					res, runErr = loop.Run(ctx)
				}
			}
			contextTokens := loop.ContextTokensEstimate()
			if runErr != nil {
				finishData := map[string]any{"role": "implementer", "error": runErr.Error(),
					"context_mode": mode, "round": round, "context_tokens_est": contextTokens}
				addRolloverFields(finishData, rolloverReason, rolloverOldTokens, newTokens)
				d.Emitter.Emit(event.SubagentFinished, finishData)
				return tools.ErrResult("implementer failed: %v\n\nSUBAGENT CONTEXT: mode=%s round=%d approx_tokens=%d", runErr, mode, round, contextTokens), nil
			}
			finishData := map[string]any{"role": "implementer", "context_mode": mode, "round": round,
				"context_tokens_est": contextTokens}
			addRolloverFields(finishData, rolloverReason, rolloverOldTokens, newTokens)
			if res.Blocked {
				finishData["blocked"] = true
			}
			d.mu.Lock()
			d.implReport = truncate(res.Report, 4096)
			d.mu.Unlock()
			d.Emitter.Emit(event.SubagentFinished, finishData)
			out := implementerOutcome(d, id, "revision", before, res)
			out.Content += subagentContextNote(mode, round, contextTokens, priorTokens)
			return out, nil
		},
	}
}

func boundedRevisionHandoff(s string) string {
	const marker = "\n…[revision handoff truncated]"
	s = strings.TrimSpace(s)
	if len(s) <= maxRevisionHandoffBytes {
		return s
	}
	return validUTF8Prefix(s, maxRevisionHandoffBytes-len(marker)) + marker
}

const defaultContextSafeFraction = 0.80

func exceedsContextBudget(spec AgentSpec, projectedTokens int) bool {
	if spec.ContextWindow <= 0 {
		return false
	}
	fraction := spec.ContextSafeFraction
	if fraction <= 0 || fraction > 1 {
		fraction = defaultContextSafeFraction
	}
	return projectedTokens >= int(float64(spec.ContextWindow)*fraction)
}

func freshImplementerLoop(d *Deps, spec AgentSpec, t *docs.Task, instructions, priorReport string) *engine.Loop {
	loop := newImplementerLoop(d, spec)
	var handoff strings.Builder
	handoff.WriteString("Coordinator revision instructions (authoritative unresolved findings, approach, and required verification):\n")
	handoff.WriteString(instructions)
	if report := strings.TrimSpace(priorReport); report != "" {
		handoff.WriteString("\n\nLatest implementer report / verification state:\n")
		handoff.WriteString(truncate(report, 4096))
	}
	seed := freshRevisePrompt(t, boundedRevisionHandoff(handoff.String()))
	if diff, err := d.Repo.Diff(); err == nil && strings.TrimSpace(diff) != "" {
		seed += "\n\nCurrent bounded workspace diff (inspect the tree for anything omitted):\n" + truncate(diff, maxDiffChars)
	}
	loop.Seed(seed)
	return loop
}

func subagentSpawnData(role string, spec AgentSpec, mode string, round, oldTokens, newTokens int, reason string) map[string]any {
	data := map[string]any{"role": role, "model": spec.Model, "logical_model": spec.Name,
		"context_mode": mode, "round": round, "prior_context_tokens_est": oldTokens}
	addRolloverFields(data, reason, oldTokens, newTokens)
	return data
}

func addRolloverFields(data map[string]any, reason string, oldTokens, newTokens int) {
	if reason == "" {
		return
	}
	data["rollover_reason"] = reason
	data["old_context_tokens_est"] = oldTokens
	data["new_context_tokens_est"] = newTokens
}

func revisionContextMode(params any) (string, error) {
	mode, _ := tools.GetString(params, "context_mode")
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return "retain", nil
	}
	if mode != "retain" && mode != "fresh" {
		return "", fmt.Errorf("context_mode must be 'retain' or 'fresh', got %q", mode)
	}
	return mode, nil
}

func newImplementerLoop(d *Deps, spec AgentSpec) *engine.Loop {
	reg := tools.New()
	reg.Add(tools.Worker(&tools.Workspace{
		Root: d.Workspace, Env: append([]string(nil), d.Env...),
		WriteRoots: tools.NormalizeRoots(d.WriteRoots), Jobs: d.Jobs,
		Emitter: d.Emitter.With("implementer"),
	})...)
	loop := d.newLoop(spec, sys(implementerSystem, false, d.Workspace), reg, "implementer")
	loop.ContextLengthHandled = true
	if loop.MaxTok < implementerMinTok {
		loop.MaxTok = implementerMinTok
	}
	return loop
}

func subagentContextNote(mode string, round, current, prior int) string {
	return fmt.Sprintf("\n\nSUBAGENT CONTEXT: mode=%s round=%d approx_tokens=%d prior_tokens=%d", mode, round, current, prior)
}

// reviewTierBlurb describes the review tiers available to the coordinator. When
// the session supplies the project's effective tiers (Deps.ReviewTiers) it names
// each one with its guidance and reviewer line-up, so a project that configures
// custom tiers (e.g. a "deep" tier with a readability reviewer and a performance
// reviewer) has them discoverable in the tool schema rather than only in config.
func reviewTierBlurb(d *Deps) string {
	var tiers []ReviewTierInfo
	if d.ReviewTiers != nil {
		tiers = d.ReviewTiers()
	}
	if len(tiers) == 0 {
		return "Match review intensity to the change via the optional review_tier: 'self-review' (you, the " +
			"coordinator, review the change yourself — NO reviewer agent is spawned; only for tiny, low-risk " +
			"changes), 'standard' (one reviewer; the sensible default for ordinary changes), or " +
			"'comprehensive' (all configured reviewers run in parallel — for large, risky, security-sensitive, " +
			"or hard-to-reverse changes). Omit review_tier to use the configured default."
	}
	var b strings.Builder
	b.WriteString("Match review intensity to the change via the optional review_tier. Available tiers:")
	for _, t := range tiers {
		b.WriteString("\n- '" + t.Name + "'")
		if t.Default {
			b.WriteString(" (default)")
		}
		if t.Description != "" {
			b.WriteString(": " + t.Description)
		}
		switch {
		case t.SelfReview:
			b.WriteString(" [no reviewer agent is spawned — you review the change yourself]")
		case len(t.Reviewers) > 0:
			b.WriteString(" [reviewers: " + strings.Join(t.Reviewers, ", ") + "]")
		}
	}
	b.WriteString("\nOmit review_tier to use the default tier.")
	return b.String()
}

func spawnReviewers(d *Deps) *gollama.Tool {
	tierNames := "e.g. self-review, standard, comprehensive"
	if d.ReviewTiers != nil {
		var names []string
		for _, t := range d.ReviewTiers() {
			names = append(names, t.Name)
		}
		if len(names) > 0 {
			tierNames = "one of: " + strings.Join(names, ", ")
		}
	}
	return &gollama.Tool{
		Name: "spawn_reviewers",
		Description: "Get independent reviews of the implementer's changes, running concurrently. " +
			reviewTierBlurb(d) + " " +
			"Returns each verdict (accept/revise) and findings; the chosen tier is recorded in session events. " +
			"Pass background:true to run the review set as a background job (returns a job_id immediately; verdicts " +
			"arrive via wait or automatically). Reviewers are read-only and run freely in parallel with other work.",
		Params: tools.Obj(map[string]any{
			"task_id":     tools.StrProp("task id"),
			"review_tier": tools.StrProp("review tier to use (" + tierNames + "); default is the configured default"),
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

			// Surface the tier selection in events. A reviewer's label may differ
			// from its model (a tier can task two focuses at the same model), so
			// both are recorded.
			revNames := make([]string, 0, len(plan.Specs))
			revModels := make([]string, 0, len(plan.Specs))
			for _, s := range plan.Specs {
				revNames = append(revNames, s.label())
				revModels = append(revModels, s.Name)
			}
			d.Emitter.Emit(event.ReviewTierSelected, map[string]any{
				"task": id, "tier": plan.Tier, "requested": plan.Requested,
				"self_review": plan.SelfReview, "fallback": plan.Fallback,
				"reviewers": revNames, "models": revModels,
			})
			if plan.SelfReview {
				d.mu.Lock()
				d.reviewers = nil
				d.mu.Unlock()
				return tools.OkResult(fmt.Sprintf("Review tier %q is a coordinator self-review: no reviewer agent was "+
					"spawned — you (the coordinator) must review this change yourself. Inspect the diff (run 'git diff'), "+
					"check it against the task's acceptance criteria, and decide whether to commit or send revisions to "+
					"the implementer.", plan.Tier)), nil
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
			preloadedDiff := buildReviewDiffHistory(d.Repo)
			hasDiff := len(preloadedDiff.History) > 0
			implementationEvidence := reviewerImplementationEvidence(d)
			d.mu.Lock()
			d.reviewers = nil
			for _, spec := range specs {
				reg := tools.New()
				reg.Add(tools.Reviewer(&tools.Workspace{Root: d.Workspace, Env: append([]string(nil), d.Env...)})...)
				actor := "reviewer:" + spec.label()
				loop := d.newLoop(spec, inspectSys(reviewerSystemFocused(spec.Focus), d.Workspace), reg, actor)
				loop.ContextLengthHandled = true
				if hasDiff {
					// Each loop owns its history slice even though every reviewer sees
					// the same stable staged snapshot.
					loop.SetHistory(append([]gollama.Message(nil), preloadedDiff.History...))
					emitSyntheticReviewDiff(d.Emitter, spec, actor, preloadedDiff)
				}
				loop.Seed(reviewerPrompt(t, spec.Focus, hasDiff) + "\n\n" + implementationEvidence)
				d.reviewers = append(d.reviewers, &reviewerHandle{name: spec.label(), model: spec.Name, spec: spec, loop: loop, round: 1, contextMode: "fresh"})
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
		Description: "Re-review after a revision. context_mode defaults to 'retain', reusing the same reviewers' " +
			"history for a small changeset; near a reviewer's configured context budget it automatically creates a fresh " +
			"replacement. Explicit 'fresh' remains useful when the revision is broad, the approach changed, or history is " +
			"obsolete. Fresh recreates the exact same resolved reviewer slots, models, focuses, reasoning settings and access " +
			"policy, preloads the current bounded diff, and seeds prior blocker/major findings plus the compact handoff.",
		Params: tools.Obj(map[string]any{
			"task_id":      tools.StrProp("task id"),
			"context_mode": tools.StrProp("optional review context strategy: 'retain' (default) or 'fresh'"),
			"handoff":      tools.StrProp("for fresh mode, concise self-contained context: what changed, prior blockers to verify, and any changed approach; bounded to 32 KiB"),
		}, "task_id"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			id, _ := tools.GetString(params, "task_id")
			mode, err := revisionContextMode(params)
			if err != nil {
				return tools.ErrResult("re_review: %v", err), nil
			}
			handoff, _ := tools.GetString(params, "handoff")
			handoff = boundedRevisionHandoff(handoff)
			d.mu.Lock()
			handles := append([]*reviewerHandle(nil), d.reviewers...)
			job := d.reviewJob
			d.mu.Unlock()
			if len(handles) == 0 {
				return tools.ErrResult("re_review: no reviewers yet; call spawn_reviewers first"), nil
			}
			if job != nil && job.Status() == jobs.Running {
				return tools.ErrResult("re_review: reviewers job %s is still running; wait for its verdicts first", job.ID()), nil
			}
			t, getErr := d.Docs.Get(id)
			if getErr != nil {
				return tools.ErrResult("re_review: %v", getErr), nil
			}
			for _, h := range handles {
				h.priorContextTokens = h.loop.ContextTokensEstimate()
				h.newContextTokens = 0
				h.rolloverReason = ""
				h.handoff = reviewerContinuationHandoff(h.lastReview, handoff)
				rollover := mode == "fresh"
				if rollover {
					h.rolloverReason = "coordinator_fresh"
				} else if exceedsContextBudget(h.spec, h.loop.ContextTokensEstimateWith(reReviewPrompt)) {
					rollover = true
					h.rolloverReason = "automatic_pressure"
				}
				if rollover {
					h.loop = freshReviewerLoop(d, h.spec, t, h.handoff)
					h.newContextTokens = h.loop.ContextTokensEstimate()
					h.contextMode = "fresh"
				} else {
					h.loop.Post(reReviewPrompt)
					h.contextMode = "retain"
				}
				h.round++
			}
			results := runReviewers(ctx, d, handles, id)
			return tools.OkResultView(aggregateReviews(results), aggregateReviewsView(results)), nil
		},
	}
}

func reviewerContinuationHandoff(previous review, supplied string) string {
	var b strings.Builder
	// Put unresolved high-severity findings first so a long coordinator narrative
	// cannot push them beyond the global handoff bound.
	var unresolved []finding
	for _, f := range previous.Findings {
		switch strings.ToLower(strings.TrimSpace(f.Severity)) {
		case "blocker", "major", "critical":
			unresolved = append(unresolved, f)
		}
	}
	if len(unresolved) > 0 {
		b.WriteString("Unresolved prior blocker/major findings to verify:\n")
		for _, f := range unresolved {
			fmt.Fprintf(&b, "- [%s] %s\n", f.Severity, f.Message)
		}
	}
	if supplied = strings.TrimSpace(supplied); supplied != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("Coordinator handoff / current verification state:\n")
		b.WriteString(supplied)
	}
	if summary := strings.TrimSpace(previous.Summary); summary != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("Prior review summary / verification state:\n")
		b.WriteString(summary)
	}
	return boundedRevisionHandoff(b.String())
}

func reviewerImplementationEvidence(d *Deps) string {
	d.mu.Lock()
	report := strings.TrimSpace(d.implReport)
	d.mu.Unlock()
	if report == "" {
		report = "(no implementer report is available; verify against the current tree and diff)"
	} else {
		const maxReportBytes = 4096
		const marker = "\n…[implementation report truncated]"
		if len(report) > maxReportBytes {
			report = validUTF8Prefix(report, maxReportBytes-len(marker)) + marker
		}
	}
	return "Latest implementation/verification evidence (implementer report):\n" + report
}

func freshReviewerHandoff(d *Deps, handoff string) string {
	evidence := reviewerImplementationEvidence(d)
	handoff = strings.TrimSpace(handoff)
	if handoff == "" {
		return evidence
	}
	const marker = "\n…[revision handoff truncated]"
	limit := maxRevisionHandoffBytes - len(evidence) - 2
	if len(handoff) > limit {
		handoff = validUTF8Prefix(handoff, limit-len(marker)) + marker
	}
	return handoff + "\n\n" + evidence
}

func freshReviewerLoop(d *Deps, spec AgentSpec, t *docs.Task, handoff string) *engine.Loop {
	preloadedDiff := buildReviewDiffHistory(d.Repo)
	hasDiff := len(preloadedDiff.History) > 0
	reg := tools.New()
	reg.Add(tools.Reviewer(&tools.Workspace{Root: d.Workspace, Env: append([]string(nil), d.Env...)})...)
	actor := "reviewer:" + spec.label()
	loop := d.newLoop(spec, inspectSys(reviewerSystemFocused(spec.Focus), d.Workspace), reg, actor)
	loop.ContextLengthHandled = true
	if hasDiff {
		loop.SetHistory(append([]gollama.Message(nil), preloadedDiff.History...))
		emitSyntheticReviewDiff(d.Emitter, spec, actor, preloadedDiff)
	}
	loop.Seed(freshReReviewPrompt(t, spec.Focus, freshReviewerHandoff(d, handoff), hasDiff))
	return loop
}

func askUser(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "ask_user",
		Description: "Ask the user one or more questions and get their answers when human input is genuinely useful. " +
			"Internal unattended runs will tell you to proceed without an answer. " +
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
		Description: "Compact the completed task, mark it done, and commit the accepted changes to git. Detailed execution remains in session events and git history.",
		Params: tools.Obj(map[string]any{
			"task_id": tools.StrProp("task id being committed"),
			"message": tools.StrProp("concise commit message"),
			"outcome": tools.StrProp("concise accepted outcome retained with the completed task"),
		}, "task_id", "message", "outcome"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			id, _ := tools.GetString(params, "task_id")
			msg, _ := tools.GetString(params, "message")
			outcome, _ := tools.GetString(params, "outcome")
			// Compact and mark done immediately before committing so the accepted tree
			// contains intent, criteria, outcome, and commit subject without duplicating
			// the detailed session history.
			if _, err := d.Docs.Complete(id, outcome, msg); err != nil {
				return tools.ErrResult("commit: %v", err), nil
			}
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
			// Starting a task also makes it the session's accounting focus.
			if status == "in_progress" {
				d.emitFocus(id)
			}
			d.Emitter.Emit(event.DocUpdated, map[string]any{"task": id, "status": status})
			return tools.OkResult(fmt.Sprintf("task %s -> %s", id, status)), nil
		},
	}
}

// runReviewers runs each reviewer's loop concurrently and waits for all (barrier),
// emitting each verdict into the session log.
func runReviewers(ctx context.Context, d *Deps, handles []*reviewerHandle, taskID string) []reviewResult {
	results := make([]reviewResult, len(handles))
	var wg sync.WaitGroup
	for i, h := range handles {
		wg.Add(1)
		go func(i int, h *reviewerHandle) {
			defer wg.Done()
			mode := h.contextMode
			if mode == "" {
				mode = "fresh"
			}
			spawnData := map[string]any{"role": "reviewer", "model": h.name, "logical_model": h.model,
				"context_mode": mode, "round": h.round, "prior_context_tokens_est": h.priorContextTokens}
			addRolloverFields(spawnData, h.rolloverReason, h.priorContextTokens, h.newContextTokens)
			d.Emitter.Emit(event.SubagentSpawned, spawnData)
			res, err := h.loop.Run(ctx)

			// Reviewers are read-only and ReviewSubmitted is emitted only below, after
			// Run returns. A context failure is therefore a safe boundary for one fresh
			// same-slot retry, even if the failed loop performed inspection tool calls.
			if err != nil && engine.IsContextLengthError(err) && ctx.Err() == nil {
				if t, getErr := d.Docs.Get(taskID); getErr == nil {
					oldTokens := h.loop.ContextTokensEstimate()
					h.loop = freshReviewerLoop(d, h.spec, t, h.handoff)
					h.priorContextTokens = oldTokens
					h.newContextTokens = h.loop.ContextTokensEstimate()
					h.rolloverReason = "context_error_recovery"
					mode = "fresh"
					recoverySpawn := map[string]any{"role": "reviewer", "model": h.name, "logical_model": h.model,
						"context_mode": mode, "round": h.round, "prior_context_tokens_est": oldTokens}
					addRolloverFields(recoverySpawn, h.rolloverReason, oldTokens, h.newContextTokens)
					d.Emitter.Emit(event.SubagentSpawned, recoverySpawn)
					res, err = h.loop.Run(ctx)
				}
			}

			rv := review{Verdict: "unknown"}
			if err != nil {
				rv.Summary = "reviewer error: " + err.Error()
			} else {
				rv = parseReview(res.Report)
			}
			if err == nil && rv.Verdict != "unknown" {
				h.lastReview = rv
			}
			h.contextMode = mode
			contextTokens := h.loop.ContextTokensEstimate()
			reviewData := map[string]any{
				"task": taskID, "model": h.name, "logical_model": h.model,
				"verdict": rv.Verdict, "summary": rv.Summary, "findings": len(rv.Findings),
				"context_mode": mode, "round": h.round, "context_tokens_est": contextTokens,
			}
			addRolloverFields(reviewData, h.rolloverReason, h.priorContextTokens, h.newContextTokens)
			d.Emitter.Emit(event.ReviewSubmitted, reviewData)
			finishData := map[string]any{"role": "reviewer", "model": h.name, "logical_model": h.model,
				"context_mode": mode, "round": h.round, "context_tokens_est": contextTokens}
			addRolloverFields(finishData, h.rolloverReason, h.priorContextTokens, h.newContextTokens)
			if err != nil {
				finishData["error"] = err.Error()
			}
			d.Emitter.Emit(event.SubagentFinished, finishData)
			results[i] = reviewResult{name: h.name, model: h.model, rv: rv, contextMode: mode,
				round: h.round, contextTokens: contextTokens, priorContextTokens: h.priorContextTokens}
		}(i, h)
	}
	wg.Wait()
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
			"and relay the answer via send_to_implementer. If no answer is available, " +
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
	name               string // display label (tier reviewer name, or the model name)
	model              string // logical model that produced the review
	rv                 review
	contextMode        string
	round              int
	contextTokens      int
	priorContextTokens int
}

// label names the reviewer for humans, disambiguating a focus label from the
// model behind it ("performance/gpt") when a tier gave the reviewer its own name.
func (r reviewResult) label() string {
	if r.model != "" && r.model != r.name {
		return r.name + "/" + r.model
	}
	return r.name
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
		fmt.Fprintf(&b, "--- %s: %s ---\n%s\n", r.label(), r.rv.Verdict, r.rv.Summary)
		for _, f := range r.rv.Findings {
			fmt.Fprintf(&b, "  - [%s] %s\n", f.Severity, f.Message)
		}
		fmt.Fprintf(&b, "  context: mode=%s round=%d approx_tokens=%d prior_tokens=%d\n", r.contextMode, r.round, r.contextTokens, r.priorContextTokens)
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
		node := tools.ViewNode{Label: r.label(), Detail: r.rv.Verdict, Kind: kind}
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
