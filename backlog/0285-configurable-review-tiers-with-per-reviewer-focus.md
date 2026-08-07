---
id: "0285"
title: Configurable review tiers with per-reviewer focus prompts and models
status: in_review
priority: 2
created: "2026-08-07"
updated: "2026-08-07"
depends_on: []
spec_refs:
    - 13.1 Review tiers
---

## Description
Make review tiers fully user-definable and let a tier task **different models with different
review focuses**: e.g. a `deep` tier running a readability/conciseness reviewer on claude and
a performance reviewer on gpt, in parallel.

Implemented:
- `[[reviews.tiers.X.reviewers]]` long form (`model`, optional `name`, `prompt` focus,
  `thinking` level) beside the existing `models = [...]` shorthand (mutually exclusive);
  tier-level `prompt` (applies to all reviewers) and `description` (coordinator guidance).
- `config.ResolvedReviewer` / `Registry.ReviewTier` / `Registry.ReviewTiers`; label
  defaulting + duplicate-label disambiguation so the same model can fill two slots.
- `orchestrator.AgentSpec.Label`/`.Focus`; reviewer system prompt gains a focus block
  (lens, not blinker); actor `reviewer:<label>`; work log records `label (model)` and
  `review (label/model): verdict`; `review_tier_selected` carries labels + models.
- `spawn_reviewers` tool description is generated from the project's effective tiers so
  custom tiers are discoverable; coordinator prompts point at that list instead of
  hard-coding the three built-ins.
- Validation: unknown/missing reviewer model, invalid per-reviewer thinking level, and
  `models` + `reviewers` both set are config errors.

## Acceptance criteria
- [x] A tier can assign a distinct model and a distinct focus prompt per reviewer
- [x] The same logical model can fill several reviewer slots under distinct labels
- [x] Built-in tiers and existing `models = [...]` configs behave exactly as before
- [x] Tier list + focuses are visible to the coordinator via the tool description
- [x] Config validation rejects malformed tiers; spec §13.1 updated
- [x] `go test ./...` green


## Work log
