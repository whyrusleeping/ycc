---
id: "0324"
title: Work-task preflight for external gates and oversized acceptance scope
status: proposed
priority: 2
created: "2026-08-12"
updated: "2026-08-12"
depends_on: []
spec_refs:
    - §6.2 Backlog
    - §10 Work orchestration
    - §11 Questions, unattended work, and confirmation
---

## Description
Before spawning a mutating implementer, have the coordinator classify acceptance criteria as locally verifiable now, externally gated (hardware/time/credentials/human), or separable. If a mandatory external criterion cannot be executed in the current environment, stop before broad implementation or split preparatory implementation from the evidence-gated completion when that split preserves user intent. Flag tasks whose acceptance surface is likely to require many independent implementation/review cycles and prefer an explicit user-approved split over a monolithic session.

Acceptance criteria:
- Work mode records a concise preflight classification before implementation for tasks containing external/temporal/hardware gates.
- Unavailable mandatory gates lead to an early structured in_review/blocked outcome or a clearly proposed split; the agent does not claim completion based on a runbook or short preflight substitute.
- Unattended mode may make reversible local assumptions but cannot waive a mandatory gate.
- The work-loop digest explains why the task stopped and what exact evidence is still required.
- Tests cover a 24-hour soak criterion, unavailable hardware, a credential gate, and a fully local task.

## Acceptance criteria

## Work log
