---
id: "0276"
title: Cross-process id reservation for backlog task creation
status: proposed
priority: 4
created: "2026-08-06"
updated: "2026-08-06"
depends_on: []
spec_refs:
    - 6.2 Backlog — structured items, markdown-rendered
---

## Description

`docs.Store` assigns ids as `max+1` under a *per-process* directory mutex (`lockFor`), so two
ycc processes on the same machine (daemon + `ycc task add`, or two daemons) can still race and
mint the same id. Duplicate ids are now auto-healed (dedupe.go, spec §6.2), so this is
hardening rather than a bug fix: prevention avoids the file churn and the ambiguous
`depends_on` guess that healing has to make.

Sketch: take an OS-level exclusive lock (flock on `backlog/.lock`, `O_EXCL` marker file with a
stale-lock timeout on platforms without flock) around the scan+write in `createLocked`, nested
inside the existing in-process mutex. Cross-checkout collisions (git merges of two branches)
remain out of scope — those are exactly what the healing pass is for.

## Acceptance criteria

- [ ] Concurrent `ycc task add` from two processes against the same backlog never produce a duplicate id.
- [ ] A crashed process cannot wedge backlog creation (stale lock is recoverable/bounded).
- [ ] Existing in-process concurrency test still passes; no lock is held across model calls.

## Work log
