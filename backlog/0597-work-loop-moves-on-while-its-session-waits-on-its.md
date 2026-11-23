---
id: "0597"
title: Work loop moves on while its session waits on its own long-running jobs
status: todo
priority: 2
created: "2026-11-23"
updated: "2026-11-23"
depends_on: []
spec_refs:
    - spec.md#9.1 Unattended work loop
---

## Description
Status: todo. Second step of the parallel work-loop plan (user decision 2026-11-23, session s_a96d1f53776eaa46). It fixes a stuck loop at N=1, and the parallel-slot task depends on it.

## Why
The work loop treats a session that is idle but waiting on its own background jobs as unfinished (`realRunSession`, `StatusWithJobContinuation`). This protects those jobs from reclamation, but it holds the whole loop. In wheeler, the user's data-pipeline project, loop `loop_b65a97db` started s_855dfad8937a75a4 at 2026-11-23 07:57 UTC. At 08:00 the session called `wait(job_1, timeout_s 70000)` on a monitor for a run due at 00:30 UTC. `.ycc/workloop.json` still read `running` more than 13 hours later, and the user started sessions by hand instead.

User decision (2026-11-23, s_a96d1f53776eaa46): the loop's parallelism counts only actively working loop sessions, and waiting sessions are capped separately.

## Acceptance criteria
- A loop session that is idle and waiting only on background shell jobs counts as waiting. The loop then starts its next session if ready, unheld work exists. A session waiting on subagent runs still counts as working.
- A waiting session keeps its task hold and file claims, stays live, and is woken by its jobs as today.
- A cap on concurrently waiting loop sessions (configurable, small default). At the cap, the loop waits for one to finish, shown with its own wait kind.
- When a waiting session later finishes, its commits, usage, verdicts and report count toward the loop digest and budgets.
- Graceful stop starts no new sessions and does not kill waiting sessions or their jobs. The outcome names the sessions still waiting.
- Persisted loop state records waiting sessions, so a restored, interrupted loop reports them.
- Tests cover a session waiting on a shell job (the loop proceeds) and one waiting on a subagent (the loop waits), plus the cap.
- spec.md §9.1 records the behavior.

## Work log
