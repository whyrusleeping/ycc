---
id: "0095"
title: Onboarding mode should read existing spec/backlog first and orient from them
status: done
priority: 3
created: "2026-06-30"
updated: "2026-07-02"
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

## Plan

Revise `onboardPresetPrompt` in internal/orchestrator/prompts.go (and its doc comment, plus the preset blurb in modes.go if needed) so the onboarding agent:

1. STEP 0 — orient from existing docs first: before any greenfield/brownfield decision, inspect for existing ycc docs — spec.md at the workspace root (Read), backlog tasks (list_backlog / get_task), and saved plans (list_plans). 
2. If usable docs exist: read them, summarize the current documented state back to the user, and continue onboarding from that base — extend/refresh the spec and backlog rather than re-establishing from scratch or creating duplicate tasks.
3. Only when no usable docs exist (no spec.md or an empty one, and no backlog tasks): fall through to the existing greenfield vs brownfield first-time flow, which stays as-is (full scoping conversation vs scoped intake).
4. Keep the wording style consistent with the surrounding prompt constants (second person, terse imperative, same formatting conventions). Adjust the leading sentence that currently asserts "the workspace has no ycc docs yet" since that assumption is being removed. Update the comment above the const accordingly. Also check the "Onboard this project" preset description in modes.go — tweak only if it contradicts the new behavior (it currently says "Establish spec.md + backlog — greenfield … or brownfield …", which may deserve a light touch like mentioning orientation from existing docs, but keep it short).
5. Verify TestOnboardPromptCoversBothBranches (modes_test.go) still passes — the prompt must keep mentioning greenfield and brownfield. Optionally extend that test (or add a small one) asserting the prompt mentions checking existing spec/backlog first (e.g. contains "spec.md", "list_backlog", "list_plans").
6. Run go build ./... and go test ./internal/orchestrator/...

### Starting points
- internal/orchestrator/prompts.go — onboardPresetPrompt const (~line 232-258)
- internal/orchestrator/modes.go line 49 — the onboard Presets() entry
- internal/orchestrator/modes_test.go — TestOnboardPromptCoversBothBranches keys on greenfield/brownfield keywords
- pm mode already has Read, list_backlog, get_task, list_plans tools available

## Work log


## Acceptance criteria

## Work log
- 2026-07-02 plan: Revise `onboardPresetPrompt` in internal/orchestrator/prompts.go (and its doc comment, plus the preset blurb in modes.go if needed) so the onboarding agent:  1. STEP 0 — orient from existing docs fi
…[truncated]
- 2026-07-02 context hints: 4 recorded with plan
- 2026-07-02 context hints: internal/orchestrator/prompts.go — onboardPresetPrompt const (~line 232-258); internal/orchestrator/modes.go line 49 — the onboard Presets() entry; internal/orchestrator/modes_test.go — TestOnbo
…[truncated]
- 2026-07-02 implementer report: Revised the onboarding preset so it orients from existing docs before deciding how to proceed.  Changes: - `internal/orchestrator/prompts.go` — rewrote `onboardPresetPrompt` and its doc comment. Rem
…[truncated]
- 2026-07-02 review tier: simple (coordinator self-review)
- 2026-07-02 decision: accept — commit: orchestrator: onboarding orients from existing spec/backlog/plans first, falling back to greenfield/brownfield only when no docs exist (0095)
