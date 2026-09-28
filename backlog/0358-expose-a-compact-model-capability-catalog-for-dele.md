---
id: "0358"
title: Expose a compact model capability catalog for delegation decisions
status: done
priority: 3
created: "2026-09-08"
updated: "2026-09-28"
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

## Outcome

spawn_agent's model parameter now lists enabled logical names (enum) plus a bounded catalog (≤16 lines, ≤2.5KB): backend/model id, subscription auth mode, effective context window, live reasoning setting, known pricing, operator-supplied modalities and labeled operator notes (new optional TOML `notes`/`modalities`, preserved by UpsertModel). Unknowns render explicitly; base_url/key_env/secrets never included; disabled models omitted and spawn validation stays live. Tests cover aliases, disabled, missing metadata, redaction, bounds, persistence; spec §13 updated.

Commit: spawn_agent: expose a bounded model capability catalog in model param metadata
