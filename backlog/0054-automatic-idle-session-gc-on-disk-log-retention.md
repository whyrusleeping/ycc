---
id: "0054"
title: Automatic idle-session GC + on-disk log retention
status: done
priority: 3
created: "2026-06-28"
updated: "2026-06-29"
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
- 2026-06-29 plan: Add an automatic, configurable, conservative-by-default GC reaper to the session Manager, plus opt-in on-disk log retention. All time logic lives in the reaper behind an injectable clock for fake-cloc
…[truncated]
- 2026-06-29 implementer report: Implemented automatic idle-session GC + opt-in on-disk log retention for the session Manager (task 0054), all behind an injectable clock for deterministic testing.  ## Changes  **internal/session/inte
…[truncated]
- 2026-06-29 review tier: single-opus — reviewers: Claude
- 2026-06-29 review (Claude): accept — The change implements a configurable, conservative-by-default background reaper for the session Manager plus opt-in age-based on-disk log retention, fully meeting task 0054's acceptance criteria. "Idl
…[truncated]
- 2026-06-29 decision: accept — commit: Add automatic idle-session GC reaper + opt-in on-disk log retention (0054)  Background reaper on the session Manager stops sessions left idle past a configurable timeout (idle status, no pending ask_u
…[truncated]
