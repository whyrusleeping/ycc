---
id: "0078"
title: 'Spike: design parallel agent workstreams via git worktrees'
status: todo
priority: 4
created: "2026-06-30"
updated: "2026-06-30"
depends_on: []
spec_refs: []
---

## Description
## Context

We want to support running multiple agent workstreams in parallel and integrating their results back together — e.g. each workstream operating in its own git worktree that later gets merged into the main branch. The exact approach and UX are not yet decided; this is an exploratory spike to figure out the right design before committing to implementation.

## Goal

Investigate and propose how parallel agent workstreams should work, with a focus on:

- **Isolation mechanism**: git worktrees vs. branches vs. separate clones; how each workstream gets its own working tree without stepping on others.
- **Integration/merge strategy**: how completed workstreams are merged back (auto-merge, sequential rebase, manual review, conflict handling).
- **UX**: how a user spawns, monitors, and reconciles parallel workstreams in the TUI / RPC; how progress and conflicts are surfaced.
- **Session/state model**: how this interacts with existing session and sandbox concepts.

## Deliverable

A short written design proposal (recommended approach + rejected alternatives + rough implementation outline), plus follow-up task(s) for the chosen direction.

## Acceptance Criteria

- [ ] Design doc / write-up enumerating at least 2 candidate approaches with tradeoffs.
- [ ] A recommended approach is identified with rationale.
- [ ] Worktree lifecycle (create → work → merge → cleanup) and conflict handling are addressed.
- [ ] Proposed UX for spawning/monitoring/merging workstreams is sketched.
- [ ] Concrete follow-up implementation task(s) are filed based on the recommendation.

## Acceptance criteria

## Work log
