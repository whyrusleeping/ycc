---
id: "0132"
title: 'Background subagents: background:true on spawn tools + wait/choose-when-to-wait'
status: done
priority: 2
created: "2026-07-04"
updated: "2026-07-04"
depends_on:
    - "0131"
spec_refs:
    - 7.3 Subagents
    - 'docs/design/async-jobs.md#3.4 Safety: the single-writer invariant'
---

## Description
Extend the job abstraction (docs/design/async-jobs.md, task 0131) to subagents so the coordinator can run several agents in parallel and choose when to wait.

Scope:
- `spawn_implementer` / `spawn_reviewers` gain `background: true` → return a job_id immediately; the child loop runs in a goroutine with its own actor-tagged emitter (`Emitter.With`), registered as an agent-kind job in the `internal/jobs` registry.
- Final report = what the synchronous path returns today (implementer report + staged diff; aggregated review verdicts), delivered exactly once via `wait` or checkpoint injection (shared mechanism from 0131). `job_output` on an agent job returns a status/progress note.
- Single-writer guard: refuse a background implementer while another mutating job (implementer or mutating background bash) is live in the same tree, with a clear error pointing at workstreams (§14.1). Read-only reviewer jobs run freely in parallel.
- Revise flows: `send_to_implementer` / `re_review` keep working — retained child loops are addressable whether they were spawned foreground or background (job registry holds the loop handle; a revise of a still-running job returns a clear error).
- Events: agent jobs emit `job_started`/`job_finished` with a `job_id` AND keep the existing `subagent_spawned`/`subagent_finished` shape (plus job_id field) so existing projections keep working. Verify `ReplayHistory` correctness with interleaved concurrent child-actor events (design says it's safe — prove it with a test).
- Coordinator prompt guidance: foreground when the result gates your next step; background only when you have genuinely independent work; never poll.

Acceptance criteria:
- Coordinator can spawn a background reviewer set and a background implementer-on-another-concern (or two reviewer jobs), keep issuing tool calls, then `wait` for any/all and receive the same reports the synchronous path produces.
- A finished background agent job not covered by a `wait` is injected at the next checkpoint.
- Spawning a second mutating job in the same tree is refused with the workstream hint.
- Interleaved concurrent subagent events replay to a valid coordinator history (test).
- Foreground (default) behavior of all spawn tools is unchanged.

## Acceptance criteria

## Plan

Extend the 0131 jobs mechanism to subagents so the coordinator can run agents in the background and choose when to wait.

1. internal/jobs: mutating-job support
   - Add a `mutates bool` field to Job (+ `Mutates()` accessor) and a way to start a mutating job (e.g. `Registry.StartMutating(kind, label, owner)` alongside the existing `Start`).
   - Add `Registry.LiveMutating() *Job` (or `(id, label, ok)`): returns a currently-running mutating job, nil/false if none. Used by the single-writer guard.

2. internal/tools/worker.go: `startBackgroundBash` registers its job as MUTATING (unsandboxed background bash may write to the tree; conservative for the guard). No behavior change otherwise.

3. internal/orchestrator/orchestrator.go: `background: true` on the spawn tools
   - spawn_implementer gains an optional bool param `background`.
     * Single-writer guard: a BACKGROUND spawn is refused while ANY live mutating job exists (implementer agent job or mutating background bash); a FOREGROUND spawn is refused only while a live mutating AGENT job exists (two implementers in one tree can never be allowed; a foreground spawn alongside live background bash keeps working as it does today). Error message must name the live job and point at workstreams (spec §14.1) for parallel mutating work, e.g. "another mutating job (job_3: implementer 0042) is live in this tree; wait for it or kill_job it, or route parallel mutating work through a separate workstream (spec §14.1)".
     * Background path: build the loop exactly as the foreground path does (same system prompt, seed, MaxTok floor, d.impl retained for later send_to_implementer), capture the `before` diff, then register `job := d.Jobs.StartMutating("agent", "implementer <task>", d.Emitter.Actor())`, emit job_started (id/kind/label) and subagent_spawned with a job_id field, and run the loop in a goroutine under job.Context() (so kill_job/session-end cancel it). On completion compute the SAME outcome text the sync path produces (reuse implementerOutcome; on Run error produce "implementer failed: …"), job.Finish(Done/Failed, text) and, if this call finalized it, emit job_finished; always emit subagent_finished (with job_id and error/blocked flags as today). Tool returns immediately: "started background job job_N …; report arrives automatically or via wait".
     * Track the live implementer job on Deps (e.g. d.implJob) so revise flows can check it.
     * If background requested but d.Jobs == nil → clear ErrResult (not available in this session).
   - send_to_implementer: if the implementer's job is still Running → clear ErrResult ("implementer job job_N is still running; wait for its report first"). Once finished, the retained loop is addressable exactly as today, whether it was spawned foreground or background.
   - spawn_reviewers gains optional `background` too. Self-review tier ignores background (returns the self-review instruction as today). Background path: build the reviewer handles exactly as today (d.reviewers replaced — note latest-set-wins for re_review), register a NON-mutating job ("agent", "reviewers <task>"), emit job_started, run runReviewers in a goroutine under job.Context(), Finish(Done, aggregateReviews(results)) and emit job_finished. Reviewer jobs are read-only and run freely in parallel with anything.
   - re_review: refuse with a clear error while the reviewer job is still Running.

4. internal/tools/jobs.go: job_output on an agent job with no buffered output should return a helpful status note ("agent job — no incremental output; its report arrives via wait or automatically") rather than the bash-flavored "(no new output since last read)".

5. Prompt guidance (internal/orchestrator/prompts.go, coordinatorSystem): add a BACKGROUND SUBAGENTS paragraph — spawn tools accept background:true and return a job_id; foreground when the result gates your next step; background only for genuinely independent work; never poll (reports arrive automatically or via wait); one mutating job per tree — parallel mutating work goes through workstreams. Update the two spawn tools' descriptions for the new param.

6. Replay: the JobStarted/JobFinished/JobNotified handling in engine/replay.go is kind-agnostic, so lost-job synthesis already covers agent jobs. Add an engine test proving ReplayHistory correctness with INTERLEAVED concurrent child-actor events: coordinator turns + tool calls interleaved with implementer and reviewer:* model_turn/tool_call/tool_result events (as concurrent jobs would record them) must replay to a valid coordinator-only history — subagent events excluded, every coordinator tool_use answered, job_notified injected as a user message at its recorded position.

7. Tests (orchestrator package, using the existing scripted-turner harness in revise_test.go):
   - Background implementer: returns job id immediately; registry Wait yields the same report text the sync path produces (implementer report + staged diff); job_started/job_finished and subagent_spawned/subagent_finished (with job_id) all emitted; DrainFinished("coordinator") delivers the report when no wait covered it (checkpoint-injection path).
   - Single-writer guard: while a background implementer job is live (use a turner that blocks on a channel), a second background spawn AND a foreground spawn are refused with the workstream hint; send_to_implementer returns the still-running error; a live mutating background bash job (StartMutating in the registry) also refuses a background implementer.
   - Background reviewers: returns job id; wait yields aggregated verdicts; re_review during a live reviewer job errors clearly.
   - Foreground defaults: existing tests must keep passing unchanged.

Verify with go build ./... && go test ./...

### Starting points
- internal/jobs/jobs.go — Registry.Start/Wait/DrainFinished/consume (exactly-once); add mutates flag + LiveMutating
- internal/orchestrator/orchestrator.go — spawnImplementer/spawnReviewers/sendToImplementer/reReview, implementerOutcome, runReviewers, Deps (d.impl, d.reviewers, d.Jobs)
- internal/tools/worker.go — startBackgroundBash + emitJobFinished pattern to mirror for agent jobs
- internal/tools/jobs.go — jobOutputTool/waitTool/FormatJobReport
- internal/engine/replay.go — ReplayHistory actor filtering + JobStarted/JobFinished/JobNotified handling
- internal/session/session.go drainJobNotes — drains owner "coordinator"; agent jobs started by coordinator tool calls are covered automatically
- internal/orchestrator/revise_test.go — scripted turner test harness (call/text helpers, captureRec)
- docs/design/async-jobs.md §3.2–3.5 — the contract this task implements

## Work log
- 2026-07-04 plan: Extend the 0131 jobs mechanism to subagents so the coordinator can run agents in the background and choose when to wait.  1. internal/jobs: mutating-job support    - Add a `mutates bool` field to Job 
…[truncated]
- 2026-07-04 context hints: 8 recorded with plan
- 2026-07-04 context hints: internal/jobs/jobs.go — Registry.Start/Wait/DrainFinished/consume (exactly-once); add mutates flag + LiveMutating; internal/orchestrator/orchestrator.go — spawnImplementer/spawnReviewers/sendToImp
…[truncated]
- 2026-07-04 implementer report: Implemented Task 0132: background subagents (`background:true` on spawn tools) with wait/choose-when-to-wait, extending the 0131 job abstraction to agent jobs.  ## Changes  **internal/jobs/jobs.go** �
…[truncated]
- 2026-07-04 review tier: single-opus — reviewers: Claude
- 2026-07-04 review (Claude): accept — The change correctly and completely implements Task 0132. It extends the jobs registry with a mutating flag / StartMutating / LiveMutating, adds background:true to both spawn tools with a correct sing
…[truncated]
- 2026-07-04 decision: accept — commit: background subagents: background:true on spawn tools, agent jobs in registry, single-writer guard, wait/checkpoint delivery (task 0132)
