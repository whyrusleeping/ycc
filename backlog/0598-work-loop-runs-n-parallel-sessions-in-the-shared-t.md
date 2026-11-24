---
id: "0598"
title: Work loop runs N parallel sessions in the shared tree (experimental)
status: done
priority: 3
created: "2026-11-23"
updated: "2026-11-24"
depends_on:
    - "0596"
    - "0597"
spec_refs:
    - spec.md#9.1 Unattended work loop
---

## Description

Status: todo. Waits on 0596 (atomic task claims and a sibling-aware loop prompt) and 0597 (the loop moves on while a session waits on its jobs). Third step of the parallel work-loop plan.

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

## Outcome

[work_loop] parallel (default 1, max 16) plus a StartWorkLoop override runs up to N working loop sessions in the shared tree (experimental above 1). Each slot runs in its own goroutine. A new start waits for the latest session's first claim, or for that session to end, with a 10-minute fallback. Stall backoff and patience are per slot; idle slots age together, and the loop stalls only once every slot has given up. Provider backoff is shared. Loop caps count the usage so far of live sessions. Breach, failure, stop and shutdown drain the working slots, and a session's own failure outranks the other outcomes. Nothing is admitted while a slot or a woken waiter has ended with its record unpublished. Wake admission and detach handoff are serialized. workloop.json v2 records slots, parallel and manual landings (v1 still restores). The snapshot, proto, web and iOS show slots and 'k/N working, m waiting'. The digest lists manual landings. The spec §9.1 hunks landed separately. Verified: Go suites with race; N=3 fake-runner tests, each guard mutation-checked; web 489 tests and bundle; YccKit tests and app build on the Mac. opus and sol accepted (round 5).

Commit: Work loop runs N parallel slots in the shared tree (0598)
