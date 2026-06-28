---
id: "0042"
title: Select different model ids sharing one provider's credentials (opus/sonnet/haiku)
status: in_progress
priority: 2
created: "2026-06-27"
updated: "2026-06-28"
depends_on:
    - "0041"
spec_refs:
    - Backends & model registry
    - Client UI (TUI)
---

## Description
## Description

Make it easy to run **different model ids that share one provider's credentials/endpoint** —
e.g. select Claude **opus / sonnet / haiku** all backed by the same Anthropic `base_url` +
`ANTHROPIC_API_KEY`. Today a logical model name conflates credentials and a single model id
(`claude` ⇒ opus-4.8), so using sonnet means hand-authoring a whole second `[models.X]` block
and re-entering the credential.

Per spec §13, a logical model is conceptually *credentials/endpoint + model id*, and several
logical models may share one credential while pointing at different model ids. This task makes
that ergonomic.

### Design (first cut — no schema change)

- Keep the flat per-logical-model config map. Add a **"duplicate"** affordance to the backend
  manager (task 0041): from an existing model, create a sibling that **reuses its backend /
  base_url / key_env / pricing** and changes only the **name + model id** (e.g. `claude-opus`
  → `claude-sonnet`). This is the "same backing anthropic token, different model" path with no
  re-entry of credentials.
- Per-backend **known model-id presets** (a small built-in list per backend, e.g. common
  Anthropic/OpenAI ids) offered as suggestions in the model-id field, with free-text entry
  retained so any id works. (Nice-to-have; free text alone is acceptable.)
- Role pickers (settings overlay §18.2) then naturally list all siblings, so a role can be set
  to `claude-sonnet` vs `claude-opus`.

### Possible future normalization (out of scope, note only)
A dedicated `[providers.X]` credential table that `[models.Y]` reference (inheriting
backend/base_url/key_env/pricing) would remove the duplicated credential per sibling. Spec §13
notes this as optional future work; this task deliberately does NOT require it.

## Acceptance criteria
- [x] From the backend manager the user can create a model variant that shares an existing
      model's backend/base_url/key_env (and pricing) and differs only in name + model id,
      without re-entering credentials.
- [x] The new variant is assignable to any role (coordinator/implementer/reviewers) and the
      next turn/spawn uses its model id under the shared credential.
- [x] Usage/cost attribution still works per logical model name (variants are distinct names).
- [x] (Nice-to-have) per-backend model-id suggestions are offered, with free-text entry kept.
- [x] Tests cover creating a sibling and resolving it through `Registry.Build` (correct model
      id, shared key_env).

## Notes
- Depends on task 0041 (backend manager UI + Upsert/Remove RPCs + mutable registry).

## Acceptance criteria

## Work log
- 2026-06-28 plan: Most of 0042 is already delivered by 0044's duplicate flow (a sibling reuses backend/base_url/key_env/pricing, differs only in name+model id, is assignable to roles, attributed per name). Close the re
…[truncated]
- 2026-06-28 done: Added config test TestModelSiblingSharesCredentials proving a sibling
  ("claude-sonnet") copying the base "claude" credential but with a distinct model id resolves
  through Registry.Build to its own model id under the shared key_env, and inherits/round-trips
  pricing (distinct per-name cost attribution). Added per-backend model-id presets
  (mbModelPresets) in the TUI backend-manager model field — ctrl+n/ctrl+p cycle suggestions while
  free-text entry is retained, with a dim presets hint under the focused model field. Tests:
  TestModelBackendsModelPresets (preset cycling + free text) and TestModelBackendsDuplicatePricing
  (duplicate carries shared base_url/key_env + identical pricing under a new name). No proto/schema
  change. go build ./... and full go test ./... pass.
- 2026-06-28 implementer report: Implemented task 0042 (run different model ids sharing one provider's credentials).  Changes: 1. internal/config/config_test.go — added TestModelSiblingSharesCredentials: starting from the base "cla
…[truncated]
- 2026-06-28 review tier: single-opus — reviewers: claude
- 2026-06-28 review (claude): accept — The staged task-0042 change satisfies the acceptance criteria. The duplicate flow (from 0044) lets a sibling reuse an existing model's backend/base_url/key_env and pricing while changing only name + m
…[truncated]
- 2026-06-28 decision: accept — commit bb1a13a: Model variants under one provider: duplicate sibling + per-backend model-id presets (§13, task 0042)  The 0044 duplicate flow already lets a sibling reuse an existing model's backend/base_url/key_env
…[truncated]
