---
id: "0044"
title: Settings-overlay "Model backends" management form (add/edit/duplicate/remove)
status: todo
priority: 2
created: "2026-06-27"
updated: "2026-06-27"
depends_on:
    - "0041"
spec_refs:
    - Client UI (TUI)
---

## Description
## Description

Add the TUI surface for managing model backends, on top of the backend RPCs delivered by
task 0041 (`UpsertModel`/`RemoveModel`/`GetModelConfig` + `ModelConfig` message). Today the
settings overlay (task 0012) only lets you *pick among already-configured* logical models per
role. This task adds a "Model backends" entry to the settings overlay that opens a sub-modal
to **list / add / edit / duplicate / remove** logical model backends, wired to the 0041 RPCs.

### Design
- New overlay row "Model backends" opens a backends modal listing the configured logical
  models (from `ListModels`). Keys: add, edit, duplicate, remove, esc/back.
- Add/edit/duplicate open a form (name, backend anthropic|openai|ollama, base_url, model id,
  key_env, optional pricing + thinking/effort/thinking_display). Reuse the first-run wizard's
  provider-form field/layout patterns (internal/setup/wizard.go) rather than inventing a new
  one. Edit/duplicate pre-fill via `GetModelConfig`.
- A "persist to ycc.toml" toggle on the form selects `persist=true/false` on `UpsertModel`.
- Remove calls `RemoveModel`; surface validation errors (e.g. removing a model a role still
  references) inline. After any change, refresh `ListModels` so role pickers see the update.
- Keys are entered/stored as `key_env` references only — never inline secrets.

## Acceptance criteria
- [ ] Settings overlay has a "Model backends" entry that lists configured logical models.
- [ ] User can add a new backend (all fields), edit an existing one, duplicate one to a new
      name, and remove one — all via `UpsertModel`/`RemoveModel`/`GetModelConfig`.
- [ ] A persist toggle controls whether the change is written to `ycc.toml`.
- [ ] Validation errors (e.g. removing a role-referenced model) are surfaced clearly inline.
- [ ] After a change, role pickers reflect the new/edited/removed model (ListModels refresh).
- [ ] TUI unit tests drive the add/edit/remove flow against a fake client.

## Notes
- Depends on 0041 (backend RPCs + registry mutation). Pairs with 0042 (model variants).

## Acceptance criteria

## Work log
