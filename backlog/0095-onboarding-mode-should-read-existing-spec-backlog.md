---
id: "0095"
title: Onboarding mode should read existing spec/backlog first and orient from them
status: todo
priority: 3
created: "2026-06-30"
updated: "2026-06-30"
depends_on: []
spec_refs:
    - §9 Modes
    - §19.2 Onboarding
---

## Description
## Description

The onboarding ("Onboard this project") flow currently assumes the workspace has **no** ycc docs yet — its opening prompt (`onboardPresetPrompt` in `internal/orchestrator/prompts.go`) explicitly states "the workspace has no ycc docs yet (no spec.md, or only an empty one, and no backlog tasks)" and jumps straight into a greenfield/brownfield decision.

The onboarding agent should instead **first look for any existing backlog and spec documents** (e.g. `spec.md` at the workspace root, tasks under `backlog/`, the backlog index, and `plans/*.md`) and, when present, use them as the **base for its orientation** rather than treating the project as undocumented. Only when no usable docs exist should it fall through to the existing greenfield/brownfield first-time scoping behavior.

Scope is the onboarding preset prompt (and any directly adjacent wording in `prompts.go`/`modes.go`). The agent already has `Read`, `list_backlog`, `get_task`, and `list_plans` available in pm mode — the change is primarily about instructing it to consult those first and ground itself in whatever already exists.

Relevant code:
- `internal/orchestrator/prompts.go` — `onboardPresetPrompt` (the opening prompt to revise)
- `internal/orchestrator/modes.go` — `Presets()` "onboard" entry / pm tool set

## Acceptance criteria

- The onboarding opening prompt instructs the agent to first inspect for existing spec.md / backlog tasks / plans (via Read, list_backlog, get_task, list_plans) before deciding how to proceed.
- When existing docs are found, the agent orients from them (reads them, summarizes current state) and continues onboarding from that base instead of re-establishing from scratch or duplicating tasks.
- When no usable docs exist, the existing greenfield vs brownfield first-time behavior still applies.
- Wording stays consistent with the surrounding presets and pm-mode framing; build and existing prompt-related tests pass.

## Work log


## Acceptance criteria

## Work log
