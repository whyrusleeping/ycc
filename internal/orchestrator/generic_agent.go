package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/sandbox"
	"github.com/whyrusleeping/ycc/internal/tools"
	"github.com/whyrusleeping/ycc/internal/workspacelease"
)

const genericAgentSystem = `You are a general-purpose read-only subagent helping another coding agent. Carry out the
prompt independently and return a concise, self-contained answer for the parent agent. Inspect the workspace as
needed, but do not modify files, git state, or durable project state. You may run read-only searches, builds, and
tests. Do not ask the user questions; state any uncertainty or missing information in your answer.`

const genericCodingAgentSystem = `You are a general-purpose coding subagent helping another coding agent. Carry out
the prompt independently in the current workspace. You may inspect and edit files and run commands, but stay tightly
within the requested scope and do not commit unless the prompt explicitly asks you to. Return a concise,
self-contained report of changes, findings, and verification for the parent agent. Do not ask the user questions;
state blockers or missing decisions in your report.`

func genericReadOnlyEnforced() bool { return sandbox.Available() != sandbox.None }

func genericModelProp(d *Deps) map[string]any {
	p := tools.StrProp("configured logical model to use for this agent")
	if d.AgentModels != nil {
		if names := d.AgentModels(); len(names) > 0 {
			p["enum"] = names
			p["description"] = "configured logical model to use; one of: " + strings.Join(names, ", ")
		}
	}
	return p
}

func spawnAgent(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "spawn_agent",
		Description: "Start a general-purpose subagent with an isolated history, an explicit prompt, and a selected " +
			"configured model. It always runs as a session background job and returns both a stable agent_id and a job_id. " +
			"Use job_output, wait, and kill_job exactly as for background Bash. Agents default to read-only inspection and " +
			"may fan out concurrently; set mutating:true only when the task must edit the workspace, subject to the existing " +
			"single-writer guard. After a turn completes, send_to_agent can retain its context or replace it with a fresh handoff " +
			"while preserving this handle's resolved model and access level.",
		Params: tools.Obj(map[string]any{
			"model":    genericModelProp(d),
			"prompt":   tools.StrProp("self-contained task or question for the subagent"),
			"mutating": tools.BoolProp("allow this agent to edit the workspace; default false. Mutating agents are serialized with mutating Bash/implementer jobs in this worktree"),
		}, "model", "prompt"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			if d.Jobs == nil {
				return tools.ErrResult("spawn_agent: background subagents are not available in this session"), nil
			}
			if d.ResolveAgent == nil {
				return tools.ErrResult("spawn_agent: model resolution is not available in this session"), nil
			}
			model, _ := tools.GetString(params, "model")
			prompt, _ := tools.GetString(params, "prompt")
			model, prompt = strings.TrimSpace(model), strings.TrimSpace(prompt)
			if model == "" || prompt == "" {
				return tools.ErrResult("spawn_agent: non-empty model and prompt are required"), nil
			}
			spec, err := d.ResolveAgent(model)
			if err != nil {
				return tools.ErrResult("spawn_agent: %v", err), nil
			}
			requestedMutating := tools.GetBool(params, "mutating", false)
			readOnly := !requestedMutating && genericReadOnlyEnforced()
			mutates := !readOnly
			if mutates {
				if live := d.Jobs.LiveMutating(); live != nil {
					return tools.ErrResult("spawn_agent: another mutating job (%s: %s) is live in this tree; wait for it or kill_job it, or route parallel mutating work through a separate workstream", live.ID(), live.Label()), nil
				}
				if !requestedMutating {
					d.Emitter.Emit(event.Narration, map[string]any{
						"msg": "generic subagent shell sandbox unavailable on this platform; treating it as a mutating job because read-only behavior is prompt-enforced only",
					})
				}
			}

			d.mu.Lock()
			d.genericSeq++
			agentID := fmt.Sprintf("agent_%d", d.genericSeq)
			d.mu.Unlock()
			actor := "agent:" + agentID
			var token *workspacelease.Token
			var lease *workspacelease.Lease
			if mutates {
				token = d.mutationToken("generic agent " + agentID)
				lease, err = d.acquireMutation(token)
				if err != nil {
					return tools.ErrResult("spawn_agent: %v", err), nil
				}
			}
			loop := newGenericAgentLoop(d, spec, actor, requestedMutating, token)
			loop.Seed(prompt)
			contextTokens := loop.ContextTokensEstimate()
			var job *jobs.Job
			var started bool
			if !mutates {
				job, started = d.Jobs.TryStartTracked("agent", agentID+" turn 1 ("+model+")", d.Emitter.Actor())
			} else {
				job, started = d.Jobs.TryStartMutatingTracked("agent", agentID+" turn 1 ("+model+")", d.Emitter.Actor())
			}
			if !started {
				lease.Release()
				return tools.ErrResult("spawn_agent: session is shutting down; agent was not started"), nil
			}
			trackAgentJob(loop, job)
			h := &genericAgentHandle{id: agentID, spec: spec, loop: loop, job: job, round: 1, mutates: mutates,
				writeAccess: requestedMutating, token: token, running: true}
			d.mu.Lock()
			if d.genericAgent == nil {
				d.genericAgent = make(map[string]*genericAgentHandle)
			}
			d.genericAgent[agentID] = h
			d.mu.Unlock()

			startGenericAgentJob(d, h, job, lease, "fresh", 1, 0, contextTokens, "")
			kind := "read-only"
			if requestedMutating {
				kind = "mutating"
			} else if mutates {
				kind = "prompt-enforced read-only"
			}
			return tools.OkResult(fmt.Sprintf("started %s subagent %s as background job %s using model %s. "+
				"Do not poll it; its report arrives automatically, or call wait([%q]) when it gates your next step. "+
				"After this turn completes, continue it with send_to_agent(agent_id=%q, prompt=..., context_mode='retain'|'fresh').%s",
				kind, agentID, job.ID(), model, job.ID(), agentID, subagentContextNote("fresh", 1, contextTokens, 0))), nil
		},
	}
}

func sendToAgent(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "send_to_agent",
		Description: "Send a follow-up to a general-purpose subagent after its previous turn has fully completed. " +
			"context_mode defaults to 'retain', which keeps conversation continuity. Use 'fresh' after context failure or when " +
			"history is obsolete: it replaces the loop without replaying prior conversation, while preserving the stable agent id, " +
			"originally resolved model, access level, and tools. A fresh prompt must be a bounded, self-contained handoff containing " +
			"the request, relevant evidence/artifact references, unresolved questions, and verification requirements. Each follow-up " +
			"is another background job; use wait/job_output/kill_job normally.",
		Params: tools.Obj(map[string]any{
			"agent_id": tools.StrProp("stable agent id returned by spawn_agent, e.g. agent_1"),
			"prompt": tools.StrProp("follow-up prompt; with fresh context, a self-contained request including relevant evidence/artifact " +
				"references, unresolved questions, and required verification; bounded to 32 KiB"),
			"context_mode": map[string]any{"type": "string", "enum": []string{"retain", "fresh"}, "description": "optional context strategy: 'retain' (default) or 'fresh'"},
		}, "agent_id", "prompt"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			if d.Jobs == nil {
				return tools.ErrResult("send_to_agent: background subagents are not available in this session"), nil
			}
			agentID, _ := tools.GetString(params, "agent_id")
			prompt, _ := tools.GetString(params, "prompt")
			agentID, prompt = strings.TrimSpace(agentID), strings.TrimSpace(prompt)
			if agentID == "" || prompt == "" {
				return tools.ErrResult("send_to_agent: non-empty agent_id and prompt are required"), nil
			}
			mode, err := revisionContextMode(params)
			if err != nil {
				return tools.ErrResult("send_to_agent: %v", err), nil
			}

			d.mu.Lock()
			h := d.genericAgent[agentID]
			if h == nil {
				d.mu.Unlock()
				return tools.ErrResult("send_to_agent: no such agent %q; call spawn_agent first", agentID), nil
			}
			if h.running {
				jobID := h.job.ID()
				mutates := h.mutates
				d.mu.Unlock()
				if mutates {
					return tools.ErrResult("send_to_agent: mutating agent %s is still running or unwinding as %s; wait for actual completion, stop it with kill_job, or use a separate workstream", agentID, jobID), nil
				}
				return tools.ErrResult("send_to_agent: agent %s is still running as %s; wait for it to finish before sending a follow-up", agentID, jobID), nil
			}
			var lease *workspacelease.Lease
			if h.mutates {
				if live := d.Jobs.LiveMutating(); live != nil {
					d.mu.Unlock()
					return tools.ErrResult("send_to_agent: another mutating job (%s: %s) is live in this tree; wait for it or kill_job it first", live.ID(), live.Label()), nil
				}
				var acquireErr error
				lease, acquireErr = d.acquireMutation(h.token)
				if acquireErr != nil {
					d.mu.Unlock()
					return tools.ErrResult("send_to_agent: %v", acquireErr), nil
				}
			}
			priorTokens := h.loop.ContextTokensEstimate()
			nextLoop := h.loop
			rolloverReason := ""
			if mode == "fresh" {
				nextLoop = newGenericAgentLoop(d, h.spec, "agent:"+h.id, h.writeAccess, h.token)
				rolloverReason = "coordinator_fresh"
			}
			round := h.round + 1
			label := fmt.Sprintf("%s turn %d (%s)", agentID, round, h.spec.Name)
			var job *jobs.Job
			var started bool
			if h.mutates {
				job, started = d.Jobs.TryStartMutatingTracked("agent", label, d.Emitter.Actor())
			} else {
				job, started = d.Jobs.TryStartTracked("agent", label, d.Emitter.Actor())
			}
			if !started {
				lease.Release()
				d.mu.Unlock()
				return tools.ErrResult("send_to_agent: session is shutting down; follow-up was not started"), nil
			}
			if mode == "fresh" {
				nextLoop.Seed(genericFreshHandoff(prompt))
			} else {
				nextLoop.Post(prompt)
			}
			contextTokens := nextLoop.ContextTokensEstimate()
			h.round = round
			h.loop = nextLoop
			trackAgentJob(nextLoop, job)
			h.job = job
			h.running = true
			d.mu.Unlock()

			startGenericAgentJob(d, h, job, lease, mode, round, priorTokens, contextTokens, rolloverReason)
			return tools.OkResult(fmt.Sprintf("started follow-up turn %d for subagent %s as background job %s with context_mode=%s. "+
				"The stable handle keeps logical model %s and its original access/tools. Do not poll it; its report arrives automatically, "+
				"or call wait([%q]) when needed.%s", round, agentID, job.ID(), mode, h.spec.Name, job.ID(),
				subagentContextNote(mode, round, contextTokens, priorTokens))), nil
		},
	}
}

func newGenericAgentLoop(d *Deps, spec AgentSpec, actor string, writeAccess bool, token *workspacelease.Token) *engine.Loop {
	reg := tools.New()
	agentWS := &tools.Workspace{Root: d.Workspace, Env: append([]string(nil), d.Env...),
		Ownership: d.Ownership, MutationToken: token}
	var system string
	if writeAccess {
		agentWS.WriteRoots = tools.NormalizeRoots(d.WriteRoots)
		agentWS.Emitter = d.Emitter.With(actor)
		reg.Add(tools.Worker(agentWS)...)
		system = sys(genericCodingAgentSystem, false, d.Workspace)
	} else {
		reg.Add(tools.ReadOnlyInspect(agentWS)...)
		system = inspectSys(genericAgentSystem, d.Workspace)
	}
	loop := d.newLoop(spec, system, reg, actor)
	loop.ContextLengthHandled = true
	return loop
}

func genericFreshHandoff(prompt string) string {
	return boundedRevisionHandoff(`Fresh-context generic-agent handoff. No prior conversation or tool log is being replayed.
Treat this bounded handoff as the complete authoritative context. It must identify the request, relevant evidence or artifact references, unresolved questions, and verification requirements (including explicit "none" where appropriate).

Caller handoff:
` + strings.TrimSpace(prompt))
}

func startGenericAgentJob(d *Deps, h *genericAgentHandle, job *jobs.Job, lease *workspacelease.Lease, mode string, round, priorTokens, newTokens int, rolloverReason string) {
	loop := h.loop
	d.Emitter.Emit(event.JobStarted, map[string]any{"id": job.ID(), "kind": job.Kind(), "label": job.Label(), "mutates": job.Mutates()})
	spawn := subagentSpawnData("generic", h.spec, mode, round, priorTokens, newTokens, rolloverReason)
	spawn["agent_id"], spawn["job_id"], spawn["mutating"] = h.id, job.ID(), h.mutates
	d.Emitter.Emit(event.SubagentSpawned, spawn)
	go func() {
		defer job.ExecutionComplete()
		defer lease.Release()
		res, err := loop.Run(job.Context())
		contextTokens := loop.ContextTokensEstimate()
		finish := map[string]any{
			"role": "generic", "agent_id": h.id, "model": h.spec.Model, "logical_model": h.spec.Name,
			"job_id": job.ID(), "round": round, "mutating": h.mutates, "context_mode": mode,
			"context_tokens_est": contextTokens,
		}
		addRolloverFields(finish, rolloverReason, priorTokens, newTokens)
		status := jobs.Done
		report := ""
		if err != nil {
			status = jobs.Failed
			report = "subagent failed: " + err.Error()
			finish["error"] = err.Error()
			if engine.IsContextLengthError(err) {
				report += fmt.Sprintf("\n\nThis loop's context is unusable. Recover with send_to_agent(agent_id=%q, context_mode=\"fresh\", prompt=\"<bounded self-contained request with evidence/artifact references, unresolved questions, and verification requirements>\"); prior conversation and tool logs will not be replayed.", h.id)
				finish["fresh_recovery_available"] = true
			}
		} else {
			report = strings.TrimSpace(res.Report)
			if report == "" {
				report = "(subagent completed without a report)"
			}
		}
		report += subagentContextNote(mode, round, contextTokens, priorTokens)
		d.Emitter.Emit(event.SubagentFinished, finish)
		d.mu.Lock()
		// Run has returned, so retained history is now safe for a follow-up. Finish
		// the job and clear running under the same handle lock: a waiter may wake as
		// Finish closes the job, but send_to_agent cannot observe a stale running
		// flag or start the next turn before this turn's finish event is emitted.
		// A killed job remains running until Run exits.
		if job.Finish(status, report) {
			emitAgentJobFinished(d.Emitter, job)
		}
		if h.job == job {
			h.running = false
		}
		d.mu.Unlock()
	}()
}
