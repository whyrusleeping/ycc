package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/whyrusleeping/gollama"
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
			"single-writer guard. Use send_to_agent after a turn completes to ask a follow-up with retained context.",
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
			reg := tools.New()
			var system string
			agentWS := &tools.Workspace{Root: d.Workspace, Env: append([]string(nil), d.Env...),
				Ownership: d.Ownership, MutationToken: token}
			if requestedMutating {
				agentWS.WriteRoots = tools.NormalizeRoots(d.WriteRoots)
				agentWS.Emitter = d.Emitter.With(actor)
				reg.Add(tools.Worker(agentWS)...)
				system = sys(genericCodingAgentSystem, false, d.Workspace)
			} else {
				reg.Add(tools.ReadOnlyInspect(agentWS)...)
				system = inspectSys(genericAgentSystem, d.Workspace)
			}
			loop := d.newLoop(spec, system, reg, actor)
			loop.Seed(prompt)
			var job *jobs.Job
			if !mutates {
				job = d.Jobs.Start("agent", agentID+" turn 1 ("+model+")", d.Emitter.Actor())
			} else {
				job = d.Jobs.StartMutating("agent", agentID+" turn 1 ("+model+")", d.Emitter.Actor())
			}
			h := &genericAgentHandle{id: agentID, spec: spec, loop: loop, job: job, round: 1, mutates: mutates, token: token, running: true}
			d.mu.Lock()
			if d.genericAgent == nil {
				d.genericAgent = make(map[string]*genericAgentHandle)
			}
			d.genericAgent[agentID] = h
			d.mu.Unlock()

			startGenericAgentJob(d, h, job, lease)
			kind := "read-only"
			if requestedMutating {
				kind = "mutating"
			} else if mutates {
				kind = "prompt-enforced read-only"
			}
			return tools.OkResult(fmt.Sprintf("started %s subagent %s as background job %s using model %s. "+
				"Do not poll it; its report arrives automatically, or call wait([%q]) when it gates your next step. "+
				"After this turn completes, continue its retained context with send_to_agent(agent_id=%q, prompt=...).",
				kind, agentID, job.ID(), model, job.ID(), agentID)), nil
		},
	}
}

func sendToAgent(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "send_to_agent",
		Description: "Send a follow-up prompt to a general-purpose subagent after its previous turn has fully completed. " +
			"The subagent retains its model, access level, tools, and conversation history. The follow-up is another background job and " +
			"returns a new job_id; use wait/job_output/kill_job normally.",
		Params: tools.Obj(map[string]any{
			"agent_id": tools.StrProp("stable agent id returned by spawn_agent, e.g. agent_1"),
			"prompt":   tools.StrProp("follow-up prompt or clarification"),
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
			h.round++
			h.loop.Post(prompt)
			label := fmt.Sprintf("%s turn %d (%s)", agentID, h.round, h.spec.Name)
			var job *jobs.Job
			if h.mutates {
				job = d.Jobs.StartMutating("agent", label, d.Emitter.Actor())
			} else {
				job = d.Jobs.Start("agent", label, d.Emitter.Actor())
			}
			h.job = job
			h.running = true
			round := h.round
			d.mu.Unlock()

			startGenericAgentJob(d, h, job, lease)
			return tools.OkResult(fmt.Sprintf("started follow-up turn %d for subagent %s as background job %s. "+
				"Do not poll it; its report arrives automatically, or call wait([%q]) when needed.",
				round, agentID, job.ID(), job.ID())), nil
		},
	}
}

func startGenericAgentJob(d *Deps, h *genericAgentHandle, job *jobs.Job, lease *workspacelease.Lease) {
	round := h.round
	d.Emitter.Emit(event.JobStarted, map[string]any{"id": job.ID(), "kind": job.Kind(), "label": job.Label()})
	d.Emitter.Emit(event.SubagentSpawned, map[string]any{
		"role": "generic", "agent_id": h.id, "model": h.spec.Model, "logical_model": h.spec.Name,
		"job_id": job.ID(), "mutating": h.mutates,
		"context_mode": map[bool]string{true: "fresh", false: "retain"}[round == 1], "round": round,
	})
	go func() {
		defer lease.Release()
		res, err := h.loop.Run(job.Context())
		finish := map[string]any{
			"role": "generic", "agent_id": h.id, "model": h.spec.Model, "logical_model": h.spec.Name,
			"job_id": job.ID(), "round": round, "mutating": h.mutates, "context_tokens_est": h.loop.ContextTokensEstimate(),
		}
		status := jobs.Done
		report := ""
		if err != nil {
			status = jobs.Failed
			report = "subagent failed: " + err.Error()
			finish["error"] = err.Error()
		} else {
			report = strings.TrimSpace(res.Report)
			if report == "" {
				report = "(subagent completed without a report)"
			}
		}
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
