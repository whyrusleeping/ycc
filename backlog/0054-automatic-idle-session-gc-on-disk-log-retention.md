---
id: "0054"
title: Automatic idle-session GC + on-disk log retention
status: todo
priority: 3
created: "2026-06-28"
updated: "2026-06-28"
depends_on:
    - "0009"
spec_refs:
    - Session & event log
    - RPC protocol
---

## Description
Follow-on from 0009 (explicit StopSession landed). Add automatic reclamation so long-lived idle sessions and orphaned on-disk logs don't accumulate.

## Context
- 0009 added `StopSession` (hard terminate: cancel ctx, close log, remove from manager map). That handles *explicit* stop, but a session that goes idle and is never interacted with still keeps its goroutine + agent loop alive for the daemon's lifetime, and on-disk logs at `<workspace>/.ycc/sessions/<id>/events.jsonl` are never pruned.
- `Manager.Stop(id)` and `Session.Stop()` are the building blocks to reuse.

## Acceptance criteria
- [ ] A background reaper (configurable interval + idle threshold) that stops sessions idle beyond a threshold — must NOT kill sessions legitimately blocked waiting for user input (e.g. interactive ask_user) or paused-to-steer; choose a safe definition of "idle" (e.g. idle status with no pending question for N minutes) and document it.
- [ ] Configurable retention/GC policy for on-disk session logs (e.g. age-based pruning), opt-in and off by default if it risks data the history view (task 0033) needs.
- [ ] Reaper is safe to disable (default conservative) and unit-tested with a fake clock / injected thresholds.
- [ ] Interaction with the durable session index (0033/0034) considered so GC doesn't delete logs a user may want to reopen.

## Acceptance criteria

## Work log
