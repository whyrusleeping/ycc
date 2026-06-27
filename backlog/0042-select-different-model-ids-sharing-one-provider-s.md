---
id: "0042"
title: Select different model ids sharing one provider's credentials (opus/sonnet/haiku)
status: todo
priority: 2
created: "2026-06-27"
updated: "2026-06-27"
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
- [ ] From the backend manager the user can create a model variant that shares an existing
      model's backend/base_url/key_env (and pricing) and differs only in name + model id,
      without re-entering credentials.
- [ ] The new variant is assignable to any role (coordinator/implementer/reviewers) and the
      next turn/spawn uses its model id under the shared credential.
- [ ] Usage/cost attribution still works per logical model name (variants are distinct names).
- [ ] (Nice-to-have) per-backend model-id suggestions are offered, with free-text entry kept.
- [ ] Tests cover creating a sibling and resolving it through `Registry.Build` (correct model
      id, shared key_env).

## Notes
- Depends on task 0041 (backend manager UI + Upsert/Remove RPCs + mutable registry).

## Acceptance criteria

## Work log
