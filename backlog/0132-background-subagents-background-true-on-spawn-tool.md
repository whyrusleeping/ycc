---
id: "0132"
title: 'Background subagents: background:true on spawn tools + wait/choose-when-to-wait'
status: todo
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

## Work log
