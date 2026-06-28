---
id: "0044"
title: Settings-overlay "Model backends" management form (add/edit/duplicate/remove)
status: done
priority: 2
created: "2026-06-27"
updated: "2026-06-28"
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
- 2026-06-28 plan: Add a "Model backends" management surface to the TUI settings overlay, wired to the existing 0041 RPCs (ListModels/GetModelConfig/UpsertModel/RemoveModel + ModelConfig).  All work in internal/tui/tui.
…[truncated]
- 2026-06-28 implementer report:
- 2026-06-28 revision: Implemented task 0044: a "Model backends" management surface in the TUI settings overlay, wired to the existing 0041 RPCs (ListModels/GetModelConfig/UpsertModel/RemoveModel + ModelConfig). All changes
…[truncated]
- 2026-06-28 review tier: single-opus — reviewers: claude
- 2026-06-28 review (claude): revise — The model-backends management modal is well-implemented and meets essentially all acceptance criteria: a "Model backends" overlay row lists configured logical models; add/edit/duplicate/remove are wir
…[truncated]
- 2026-06-28 revision: Fixed the reachable out-of-range crash when the model-backends list shrinks after a removal.  ## internal/tui/tui.go 1. In the `modelsMsg` handler in Update() (where m.models is set), clamped mbCursor
…[truncated]
- 2026-06-28 review (claude): accept — The implementation adds the "Model backends" settings-overlay surface (list/add/edit/duplicate/remove) wired to the 0041 RPCs (ListModels/GetModelConfig/UpsertModel/RemoveModel), with a persist toggle
…[truncated]
- 2026-06-28 decision: accept — commit d342185: TUI: Model backends management modal in settings overlay (add/edit/duplicate/remove) (§18.2, task 0044)  Add a "model backends" entry to the settings overlay that opens a sub-modal to list/add/edit/d
…[truncated]
- 2026-06-28 usage: 51,733 tok (in 150, out 51,583, cache_r 4,409,191, cache_w 236,564) · cost n/a (unpriced)
