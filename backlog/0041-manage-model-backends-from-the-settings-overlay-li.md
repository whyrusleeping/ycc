---
id: "0041"
title: Manage model backends from the settings overlay (live add/edit/remove, persisted)
status: done
priority: 2
created: "2026-06-27"
updated: "2026-06-27"
depends_on: []
spec_refs:
    - Client UI (TUI)
    - RPC protocol (Connect)
    - Backends & model registry
---

## Description
## Description

Let the user configure **everything about a model backend from the TUI settings overlay** —
add a new OpenAI/Ollama/Anthropic endpoint, edit an existing one, duplicate one, or remove
one — without hand-editing `ycc.toml` or re-running the first-run wizard. Today the
first-run wizard (task 0023, done) writes the initial `ycc.toml`, and the settings overlay
(task 0012, done) only lets you **pick among already-configured** logical models per role
(`SetRoleConfig`). There is no way to *create or change a backend* mid-session.

See spec §18.2 (Model backends — add/edit/remove), §12 (RPCs), §13.

### Design

- **Config / registry (`internal/config`, `internal/session`).** The daemon's config must
  become mutable at runtime. Today `config.Registry` wraps an immutable `*Config` shared by
  the `session.Manager`. Add registry methods to upsert/remove a `Model` entry (guarded by a
  mutex) and to snapshot the config for persistence. Because `Registry.Build` constructs a
  fresh client from `cfg` on every call, mutating the `Models` map makes the next `Build`
  (next coordinator turn / next spawned subagent / next `SetRoleConfig`) use the new
  backend — no restart. Reject removing/renaming a model a role still references (extend the
  existing validation). `config.Save` (task 0022, done) already writes `ycc.toml`.
- **Proto / server.** Add `UpsertModel(ModelConfig, persist)` and `RemoveModel(name, persist)`
  RPCs (spec §12). Add a `ModelConfig` message mirroring a `[models.X]` block (name, backend,
  base_url, model, key_env, thinking/effort/display, pricing pointers) and a way to read the
  full records for editing (extend `ListModels`/`ModelInfo` or add `GetModelConfig`).
  `persist=true` ⇒ `config.Save` to the discovered config path (track it on the daemon, see
  §19.1 `DiscoverConfig`); `persist=false` ⇒ live-only edit. Wire `Server` handlers to the
  registry mutators.
- **TUI (`internal/tui`).** Add a "Model backends" entry to the settings overlay that lists
  the configured logical models and supports add / edit / **duplicate** / remove via a form.
  Reuse the first-run wizard's provider-entry form components (task 0023) rather than building
  a second form. Keys are entered/stored as `key_env` references (never inline secrets), per
  the spec's keys-in-env lean. Surface validation errors (e.g. removing a model a role uses).

## Acceptance criteria
- [ ] From the settings overlay the user can **add** a new logical model backend (name,
      backend anthropic|openai|ollama, base_url, model id, key_env, optional pricing +
      thinking) and it becomes selectable for roles immediately.
- [ ] The user can **edit** and **remove** an existing backend from the overlay.
- [ ] Changes take effect without restarting the daemon (next `Build`/turn/spawn uses them).
- [ ] With "persist" chosen, the change is written to `ycc.toml` (`config.Save`) and survives
      a daemon restart; without it, only the live config changes.
- [ ] Removing/renaming a model still referenced by a role is rejected with a clear error;
      the session never points at a missing backend.
- [ ] `key_env` is stored as an env-var reference; no secret values are written to TOML.
- [ ] New `UpsertModel`/`RemoveModel` RPCs + `ModelConfig` message; server handlers + unit
      tests for the registry mutation, validation, and persist paths.

## Notes
- Depends on done tasks 0012 (settings overlay), 0022 (config.Save), 0023 (first-run wizard).
- Enables task 0042 (model variants under one provider), which adds the "duplicate to a new
  model id" affordance on top of this CRUD surface.

## Acceptance criteria

## Work log
- 2026-06-27 plan: Make the daemon config mutable and expose backend CRUD to the TUI.  1. config/registry: make `config.Registry` hold a mutable `*Config` under a mutex; add    `UpsertModel(config.Model)`, `RemoveModel(
…[truncated]
- 2026-06-27 plan: Scope: backend foundation for managing model backends at runtime. The TUI form is split out to task 0044 (depends on this). Deliver mutable registry + RPCs + persistence + tests.  1. config/registry (
…[truncated]
- 2026-06-27 implementer report: Implemented the runtime model-backend management foundation for task 0041 (TUI form excluded — that is task 0044, per the coordinator's plan).  ## Changes  **internal/config/config.go** - Added `Mod
…[truncated]
- 2026-06-27 review (claude): accept — The change implements the runtime model-backend management foundation for task 0041, which the coordinator explicitly re-scoped (TUI form split out into the new, committed task 0044). The delivered wo
…[truncated]
- 2026-06-27 decision: accept — commit 6a5feb8: Runtime model-backend CRUD: mutable registry + UpsertModel/RemoveModel/GetModelConfig RPCs (§18.2)  Make config.Registry thread-safe and mutable so model backends can be added, edited, and removed wi
…[truncated]
- 2026-06-27 usage: 38,361 tok (in 132, out 38,229, cache_r 2,454,951, cache_w 104,380) · cost n/a (unpriced)
