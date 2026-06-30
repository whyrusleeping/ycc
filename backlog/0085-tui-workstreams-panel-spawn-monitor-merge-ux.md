---
id: "0085"
title: 'TUI: Workstreams panel + spawn/monitor/merge UX'
status: todo
priority: 4
created: "2026-06-30"
updated: "2026-06-30"
depends_on:
    - "0084"
spec_refs:
    - Client UI (TUI)
    - RPC protocol (Connect)
---

## Description
## Context
Fifth step of the parallel-workstreams design (see `docs/design/parallel-workstreams.md` §8, §10.5). Add the TUI surface to spawn, monitor, and reconcile parallel workstreams.

## Scope
- Multi-select spawn from the backlog browser ("Run in parallel (N workstreams)").
- A Workstreams panel listing active workstreams with live status (running / idle / awaiting-review / conflict), focused task, branch, commit count; each row drills into the existing session view (reused unchanged).
- A merge/accept overlay showing trial-merge result (clean / conflicted paths) + integrated diff; merge / discard actions.

## Acceptance criteria
- [ ] User can spawn N workstreams from the backlog browser.
- [ ] Workstreams list shows live per-workstream status and drills into the session transcript.
- [ ] Merge overlay shows trial-merge result + integrated diff and supports merge/discard.
- [ ] Conflicts are visually distinct (a clear row state), never a silent failure.
- [ ] build/vet/test pass.

## Acceptance criteria

## Work log
