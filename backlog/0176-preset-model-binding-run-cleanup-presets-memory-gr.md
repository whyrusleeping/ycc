---
id: "0176"
title: 'Preset → model binding: run cleanup presets (memory-groom, spec-doctor) on a non-default model'
status: todo
priority: 3
created: "2026-07-08"
updated: "2026-07-08"
depends_on: []
spec_refs:
    - Modes (the home menu)
    - Backends & model registry
---

## Description
Counteract single-model "dialect drift" in agent-maintained docs (memory.md, spec.md): a model that keeps writing its own future context reinforces its own idioms, framing, and self-instructions. ycc's founding multi-perspective principle (§1, "not one model grading its own homework") should extend to doc grooming: the cleanup presets should be easy to run on a *different* model than the daily coordinator (e.g. gemini cleaning Claude-authored docs), breaking the self-reinforcement loop.

Today this is possible only manually (change the coordinator model in the settings overlay before starting the preset). The gap is a zero-friction default binding, e.g. config:

```toml
[roles.presets]
memory-groom = "gemini"
spec-doctor  = "gpt"
```

Design notes:
- Presets are client-side opening prompts over pm mode (internal/orchestrator/modes.go); the coordinator model is a session role. Needs a per-session coordinator override at StartSession (or the client applying a non-persisted role override) — must NOT persist via SetRoleConfig, which writes ycc.toml.
- Unbound presets keep today's behavior (configured coordinator role).
- A missing/unknown model name should degrade gracefully to the default with a logged warning.

## Acceptance criteria
- [ ] Optional config maps preset name → logical model; absent = current behavior.
- [ ] Starting a bound preset runs its coordinator on the bound model without persisting a role change.
- [ ] Unknown model name degrades to the default coordinator with a visible warning.
- [ ] Spec updated (§9 / §13) to document the binding and its rationale (cross-model doc grooming).

## Plan

Goal: an optional config binding preset → logical model so cleanup presets (memory-groom, spec-doctor) run their pm coordinator on a non-default model, without persisting a role change.

1. Config: add `Presets map[string]string` to `config.Roles` (`toml:"presets,omitempty"`, i.e. `[roles.presets]` with entries like `memory-groom = "gemini"`). Add a `Registry.PresetModel(name) (model string, bound bool)` accessor. Do NOT hard-fail load on an unknown model name — graceful degradation is the contract (validated at use-time).

2. Proto: add a `string preset` field to `StartSessionRequest` in proto/ycc/v1/ycc.proto; regenerate via buf (buf.gen.yaml at root). Backward-compatible (empty = no preset).

3. Session plumbing: add `Preset string` to `session.Config`; `server.StartSession` passes `m.Preset` through. In `Manager.start`, resolve the binding: if the preset is bound and `m.reg.Has(model)`, use it as the session's initial coordinator instead of `m.reg.CoordinatorName()` (thread an override into `newSession`, which currently hardcodes `coordName := m.reg.CoordinatorName()` at ~line 1300 of internal/session/session.go). If bound but unknown, fall back to the default coordinator and emit a visible warning (e.g. a session_error/notice event naming the preset, bad model, and fallback). Crucially: never call `reg.SetRoles`/`SetRoleConfig` — nothing is written to ycc.toml.

4. Reopen fidelity: include `"preset"` in the SessionStarted event data; extend `event.Reduce`'s Projection with `Preset` (internal/event/reduce.go) and have `Manager.Reopen` re-resolve the binding the same way, so a reopened preset session keeps its bound coordinator (same graceful degradation if the model was since removed).

5. TUI: `menuEntry` (internal/tui/tui.go, built from ListModes presets ~line 2318) carries the preset Name; when a preset entry starts a session, set the new `Preset` field on the StartSessionRequest.

6. Mid-session `SetRoleConfig` behavior is unchanged (it still persists, as today) — the binding only picks the initial coordinator.

7. Spec: document `[roles.presets]` and its rationale (cross-model doc grooming; breaks single-model dialect self-reinforcement) in §9 (modes/presets) and §13 (backends & roles).

8. Tests: config round-trip of `[roles.presets]`; Manager.Start with a bound preset → session coordinator == bound model AND ycc.toml roles untouched; unknown bound model → default coordinator + warning event; Reopen restores the bound coordinator; TUI preset entry sets the request field (existing tui_test harness patterns).

### Starting points
- internal/session/session.go ~1290-1310: newSession hardcodes coordName := m.reg.CoordinatorName() — the override point
- internal/session/session.go:1023 Manager.Start / 1497 Manager.Reopen; SetRoleConfig at :474 persists via reg.SetRoles — do NOT reuse it
- internal/event/reduce.go:50 Reduce() projects SessionStarted data (mode/level/workspace) — add preset
- internal/orchestrator/modes.go:54 Presets() — preset names: onboard, spec-doctor, memory-groom
- internal/tui/tui.go:2318 builds menuEntry from ListModes presets; session start composes openingPrompt ~line 3560
- proto/ycc/v1/ycc.proto StartSessionRequest; regen via buf (buf.gen.yaml)
- internal/config/config.go:199-216 Roles struct + role constants; Registry.Has / SetRoles nearby

## Work log
- 2026-07-08 plan: Goal: an optional config binding preset → logical model so cleanup presets (memory-groom, spec-doctor) run their pm coordinator on a non-default model, without persisting a role change.  1. Config: 
…[truncated]
- 2026-07-08 context hints: 7 recorded with plan
