---
id: "0598"
title: Work loop runs N parallel sessions in the shared tree (experimental)
status: todo
priority: 3
created: "2026-11-23"
updated: "2026-11-23"
depends_on:
    - "0596"
    - "0597"
spec_refs:
    - spec.md#9.1 Unattended work loop
---

## Description
Status: todo. Waits on 0596 (atomic task claims and a sibling-aware loop prompt) and 0597 (the loop moves on while a session waits on its jobs). Third step of the parallel work-loop plan.

## Why
The user wants one work loop to run a configurable number of agents in parallel, all in the shared tree. Their projects already share GPUs, databases, disks and hosts, so coordination has to improve whether or not files are isolated. In wheeler, the user's data-pipeline project, 2+ sessions overlapped in 27% of active hours (peak 7). The real collisions came from shared files and services, not shared task ids. 0596 handles choosing and claiming, and 0599 handles exclusive deploy and service access.

User decisions (2026-11-23, session s_a96d1f53776eaa46):
- Run in the shared tree, not workstreams.
- Agents choose their tasks. The next slot starts only after the previous agent has finished choosing.
- N counts actively working loop sessions only.
- N>1 may ship as experimental before 0593 (candidate-tree commits) lands, with manual landings counted in the digest.

## Acceptance criteria
- A `[work_loop] parallel` project setting (default 1) and a per-start override on `StartWorkLoop`. Values above 1 are documented as experimental until 0593 lands.
- The loop keeps up to N actively working sessions. Waiting sessions (0597) do not count and keep their separate cap.
- A new slot's session starts only after the most recently started loop session has claimed a task or ended, with a timeout fallback.
- Stall detection runs per slot, using 0596's per-session progress. The loop stalls only when every slot has stalled.
- Loop token and cost caps are checked against in-flight usage. On a breach, a non-retryable session failure, or a graceful stop, the loop starts no new sessions and lets running slots finish. Provider-outage backoff is shared across slots.
- `workloop.json` persists every slot (format version bumped). Restart still restores the loop as interrupted.
- The loop snapshot exposes slots (session, task, working or waiting). Clients show at least "k/N working, m waiting".
- The digest counts commits made during the loop that no loop session's `commit_made` recorded, as manual landings.
- A test with a fake session runner at N=3 covers: distinct claimed tasks, start gating on the previous claim, stop draining all slots, per-slot stall, and a budget breach without overshooting by N whole sessions.
- spec.md §9.1 records the behavior.

## Work log
