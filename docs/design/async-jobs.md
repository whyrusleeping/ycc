# Async jobs: background subagents & background bash

> Status: **design** (approved direction, pre-implementation). Companion to spec §7.3
> (subagents), §8 (tools), and §14.1 (parallel workstreams). Backlog: 0131 (jobs core +
> background bash), 0132 (background subagents + wait).

## 1. Problem

Today every delegation is synchronous: `spawn_implementer` blocks its tool call until the
child loop finishes; reviewers fan out concurrently but only *inside* one
`spawn_reviewers` call (a wait-all barrier the coordinator cannot see into). The
coordinator cannot:

- kick off several subagents and keep working while they run;
- choose *when* to wait (and on *which* one);
- run a long shell command (test suite, build, watcher) without stalling its own turn.

## 2. Prior art: Claude Code (~2.1.x)

Three mechanisms, in order of introduction:

1. **Parallel foreground fan-out** — multiple `Task` tool_use blocks in one assistant
   message run concurrently; all tool_results return together before the next turn.
   Wait-ALL only; no ids; no choice of when to wait.
2. **Background jobs with ids + push notification** —
   `Bash(run_in_background: true)` → shell id; `BashOutput(id)` returns output *since
   last read* + status; `KillShell(id)`. `Task(run_in_background: true)` → task id;
   `TaskOutput(id, block?)` retrieves/waits. Completion is **pushed**: the harness
   injects a notification into the conversation between turns, and the prompt forbids
   polling ("do NOT sleep, poll, or proactively check").
3. **Continue-an-agent** — `SendMessage` to an agent id resumes it with retained
   context (ycc's `send_to_implementer` / `re_review` already are this pattern).

Lesson from their issue tracker (anthropics/claude-code #20236, #20679): two competing
delivery paths — a blocking retrieval tool AND a notification queue — deadlock when the
notification piles up behind the block. **One delivery path must be authoritative.**

## 3. Design: one job abstraction for shell commands and subagents

A **job** is a unit of background work owned by the session: either a shell process or a
child agent loop. Both are addressed by the same id namespace and the same tools; only
the guts differ.

### 3.1 Job registry

New `internal/jobs` package (session-scoped, held by `orchestrator.Deps`):

```go
type Job struct {
    ID     string   // "job_<n>" (monotonic per session)
    Kind   string   // "bash" | "agent"
    Label  string   // command line, or role+task ("implementer 0042")
    Status Status   // running | done | failed | killed
    // bash: incremental output ring buffer + per-reader cursor
    // agent: the child *engine.Loop (retained for revise/re_review addressing)
    Result string   // final report: exit code + output tail, or the agent's report
    done   chan struct{}
}
```

The registry hands out ids, tracks liveness, and kills everything on session end
(coordinator loop exit ⇒ cancel all job contexts).

### 3.2 Tool surface (coordinator; background bash also for the implementer)

- `Bash(..., run_in_background: true)` → returns `job_id` immediately.
- `spawn_implementer` / `spawn_reviewers` (and any future `spawn_investigator`) gain
  `background: true` → return `job_id` immediately; the child loop runs in a goroutine
  with its own actor-tagged emitter (existing `Emitter.With` machinery).
- `job_output(job_id)` — non-blocking: output since last read (bash) or progress note
  (agent) + current status. Never consumes the final report.
- `wait(job_ids?, for: "any"|"all", timeout_s?)` — blocks; returns the final report(s)
  of the completed job(s). Empty `job_ids` ⇒ all live jobs.
- `kill_job(job_id)`.

### 3.3 Delivery of final reports: exactly once

A job's **final report** is delivered exactly once, by whichever fires first:

- a `wait(...)` call that covers it, or
- **checkpoint injection**: the loop already consults `Steer.Checkpoint` between turns
  and after every tool result and appends returned messages; a sibling hook (or the
  same hook, session-owned) drains finished-job notifications there —
  `"[job job_3 done] go test ./... — exit 0 (last 20 lines: …)"` — as a user-role
  message before the next turn.

So the model *never polls*: fire, keep working, and either the report arrives at a
checkpoint or the model calls `wait` when the result gates its next step. `job_output`
can be re-read any time and is not part of the exactly-once rule.

### 3.4 Safety: the single-writer invariant

Two agents mutating one worktree race (spec §14.1 exists precisely for this).

- **Read-only background agents** (reviewers; an explore/investigate role) run freely
  in-tree, in parallel.
- **Background implementers in the same tree are refused** by the spawn tool while
  another mutating job (implementer or mutating bash) is live there. Parallel mutating
  work routes through workstreams (linked worktrees, §14.1).
- Background bash is intended for builds/tests/watchers; the prompt says so.

### 3.5 What we deliberately do NOT build

- **Concurrent dispatch of multiple tool calls in one turn** (Claude Code mechanism
  #1): racy for mutating worker tools, and `background: true` + `wait` subsumes it.
- **A second retrieval path for final reports** (the Claude Code deadlock): `wait` and
  checkpoint injection share one consumed-flag; `job_output` never consumes.

## 4. Events, replay, UI

- New events `job_started` / `job_finished` (data: id, kind, label, status, tail);
  agent jobs additionally keep emitting `subagent_spawned`/`subagent_finished` with a
  `job_id` field so existing projections keep working.
- Child-loop events are already actor-tagged and `engine.ReplayHistory` filters by
  actor and matches tool_results by call id, so interleaved concurrent subagent events
  replay safely today. Checkpoint-injected notifications must be *recorded* (as a
  user-actor event) so reopen reconstructs the identical history — same rule as steer
  corrections.
- Reopen with jobs mid-flight: jobs do not survive a daemon restart. Replay synthesizes
  a "(job lost: daemon restarted)" report for any job whose start was recorded but
  whose finish was not, keeping histories valid. Noted v1 limitation.
- TUI: jobs get a small live-status line (id, label, spinner/exit); reviewer
  concurrency already renders interleaved actors.

## 5. Phasing

1. **Jobs core + background bash** (task 0131): registry, `run_in_background` on Bash,
   `job_output`, `kill_job`, `wait`, checkpoint injection, events + replay, kill-on-exit.
   Exercises the whole mechanism with the simplest job kind.
2. **Background subagents** (task 0132): `background: true` on spawn tools, agent jobs
   in the registry, single-writer guard, revise-flow addressing by job id,
   prompt guidance (foreground when the result gates the next step; background only
   with genuinely independent work; never poll).
3. Later: an explore/investigate read-only role (where parallel background agents pay
   off most), and workstream-scoped background implementers for true parallel coding.
