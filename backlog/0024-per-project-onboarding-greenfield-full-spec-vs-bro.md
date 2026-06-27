---
id: "0024"
title: Per-project onboarding — greenfield (full spec) vs brownfield (scoped) pm presets
status: todo
priority: 2
created: "2026-06-26"
updated: "2026-06-26"
depends_on: []
spec_refs:
    - Onboarding flows
    - Modes (the home menu)
    - Document model
    - Client UI (TUI)
---

## Description
## Description
Implement **per-project onboarding** (spec §19.2): the first time a session begins in a
workspace that has no ycc docs (no `spec.md` / `backlog/`), help the user establish the
project's `spec.md` and backlog — handling brand-new (greenfield) and existing
(brownfield) projects differently.

This is an **agent-driven `pm`-mode flow**, so it ships as new pm **presets** (opening
prompt + first message) in `internal/orchestrator` (`Presets()` + `prompts.go`), alongside
`feature`/`bug`/`spec`/`backlog`. A single "Onboard this project" entry routes to the right
behaviour; the prompt instructs the agent to determine new-vs-existing from the workspace
itself (and confirm if ambiguous).

**Greenfield (empty repo):** full scoping conversation — elicit purpose/scope/constraints,
author an initial `spec.md` (canonical sections §6.1), and seed a starter backlog. "Spec
the whole thing."

**Brownfield (substantial existing code, no docs):** SCOPED intake — do NOT spec the whole
repo. (1) Ask what the user wants to work on; (2) explore only the relevant code (Read +
ripgrep); (3) write only the spec slice(s) the work touches (mark the spec as
partial/seeded-as-needed); (4) create backlog tasks for the requested work with a concrete
`propose_plan`, ready to hand to `work` via `switch_to_work`. Guiding principle: **spec the
work, not the repo** — coverage grows incrementally.

**Discoverability (client side):** when a project/workspace is opened and looks
un-onboarded (no `spec.md` or trivially empty + no `backlog/`), surface the onboarding
entry prominently in the home menu. It remains available as a normal preset afterwards
("onboard later" is valid). Detecting un-onboarded state can be done client-side or via a
small helper; keep it simple.

Add the preset(s) to `orchestrator.Presets()`, the prompt text to `prompts.go`, and ensure
they flow through `ListModes` → home menu unchanged (no proto change needed — presets
already carry an opening prompt). Keep this distinct from `feature` (which assumes spec +
backlog already exist).

## Acceptance criteria
- [ ] New pm preset(s) for project onboarding exist in `orchestrator.Presets()` with
      opening prompts in `prompts.go`, surfaced via `ListModes`.
- [ ] The onboarding prompt instructs the agent to detect greenfield vs brownfield from the
      workspace and confirm when ambiguous.
- [ ] Greenfield branch: authors an initial `spec.md` with the canonical sections and seeds
      a starter backlog.
- [ ] Brownfield branch: scopes to the user's stated work — explores only relevant code,
      writes only the touched spec slice(s) (not a full spec), and creates backlog tasks +
      a plan for that work.
- [ ] The home menu prominently offers onboarding when the workspace has no `spec.md` /
      `backlog/`, and still offers it as a normal preset otherwise.
- [ ] Tests cover preset registration/exposure (and any un-onboarded detection helper).

## Work log


## Acceptance criteria

## Work log
