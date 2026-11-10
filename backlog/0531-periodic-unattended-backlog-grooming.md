---
id: "0531"
title: Periodic unattended backlog grooming
status: done
priority: 2
created: "2026-11-10"
updated: "2026-11-10"
depends_on:
    - "0530"
spec_refs:
    - docs/design/project-memory.md#Automatic grooming
    - docs/design/watches.md
---

## Description

## Description
## Why
The user asked for periodic background backlog grooming (2026-11-10, this session), so backlog quality doesn't depend on someone remembering to run the backlog-groom preset (task 0530). Drift builds up over bursts of agent work: wheeler's 71 "slice of accepted 0068" tasks were all created Oct 17–20, and valstore's degenerate work logs appeared over three days.

## Behaviour
- The daemon starts an unattended backlog-groom pm session per registered project. It follows the automatic memory-groom design in `docs/design/project-memory.md#Automatic grooming`: at most one per project, a persisted cooldown, a model binding (else the default coordinator, not a cheaper model), and no grooming of linked workstream worktrees.
- **Trigger:** backlog churn since the last finished groom (tasks created or updated, or work-log bytes added past a threshold) rather than wall-clock time alone, so idle projects cost nothing. Pick one trigger and justify it in the spec.
- Each run grooms a bounded batch: tasks changed since the last groom first, then the oldest tasks not yet groomed. It records progress so successive runs cover the whole backlog.
- **Unattended limits:** it applies only the fix-class changes from 0530 (debris, glosses, contradictions settled by evidence). Everything in the flag class goes to one user-visible report or follow-up per run, not a stream of questions. It never changes task status unless evidence settles it.
- A per-project config switch disables it, like `memory.auto_groom`. Clients show the last run and a "groom now" action.

## Acceptance criteria
- Scheduling, cooldown, batching and the disable switch are implemented and covered where they could regress: the one-per-project bound, cooldown persistence, and the disable switch.
- The spec describes the trigger and the limits of unattended runs.
- Web and iOS show the last groom result and offer "groom now". UI may be split out if it's large.
- A scratch-daemon run against a copied backlog shows one bounded unattended run and a correct follow-up report.

## Acceptance criteria

- Scheduling, cooldown, batching and the disable switch are implemented and covered where they could regress: the one-per-project bound, cooldown persistence, and the disable switch.
- The spec describes the trigger and the limits of unattended runs.
- Web and iOS show the last groom result and offer "groom now". UI may be split out if it's large.
- A scratch-daemon run against a copied backlog shows one bounded unattended run and a correct follow-up report.

## Outcome

At session start, if 10+ non-in_progress tasks changed since the last finished groom, the daemon starts one unattended backlog-groom pm run in the primary tree (internal/session/backloggroom.go). Limits: one per project, a persisted 24h cooldown (.ycc/backlog-groom.json), batches of 15. A finished run acknowledges only tasks that gained a groom: entry. Runs make fix-class edits only and put flags in one standing proposed task. [backlog] auto_groom switches it off per project (.ycc/config.toml) or daemon-wide (ycc.toml). Spec §6.2 documents it. Tests cover the per-project bound, cooldown persistence, the switches and baselines. Two real opus runs on a copied backlog fixed the planted issues, rewrote the single follow-up task instead of duplicating it, and skipped the in_progress task. The web/iOS UI is split into 0533. sol accepted.

Commit: Daemon-scheduled unattended backlog grooming on churn: one per project, persisted 24h cooldown, bounded batches, per-project/daemon switches (0531)
