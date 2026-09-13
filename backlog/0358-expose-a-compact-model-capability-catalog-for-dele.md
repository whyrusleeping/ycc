---
id: "0358"
title: Expose a compact model capability catalog for delegation decisions
status: blocked
priority: 3
created: "2026-09-08"
updated: "2026-09-10"
depends_on: []
spec_refs:
    - §7.3 Subagents and asynchronous jobs
    - §13 Models, credentials, and review tiers
---

## Description
spawn_agent requires selecting a logical model but genericModelProp exposes only aliases, making instructions to choose a suitable model under-informed. Evidence: internal/orchestrator/generic_agent.go:28–50. Give the parent actionable configuration metadata without invented capability rankings.

## Acceptance criteria
- Expose enabled logical names, actual configured model identities, operator-provided suitability notes, modalities, known/configured context windows, reasoning settings, and pricing where known.
- Present a compact catalog in discovery or tool metadata without repeating an unbounded model registry every turn.
- Unknown capabilities/costs remain explicitly unknown; optional measured performance is distinguished from operator advice.
- Never expose credentials, secret values, or sensitive endpoint parameters.
- Disabled/changed configurations produce clear behavior and remain consistent with spawn validation.
- Tests cover aliases, disabled models, missing metadata, secret redaction, and catalog bounds.

## Work log

- Preflight: this task's backlog file was already staged as an addition at session entry. The current changeset ownership guard (`internal/git/changeset.go`) rejects edits to baseline-dirty paths, including required task bookkeeping, as independently evidenced by task 0325's failed delegation. Configuration and orchestrator source also contain unrelated uncommitted work. No implementation, tests, or review occurred.
- Blocked on an isolated clean task worktree/session containing the accepted task, or existing owners resolving their changes before a new baseline is captured. Do not absorb or discard their staged/unstaged changes or retry delegation against this baseline. All original catalog criteria remain outstanding.
