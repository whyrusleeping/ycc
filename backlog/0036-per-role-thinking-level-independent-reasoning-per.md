---
id: "0036"
title: Per-role thinking level (independent reasoning per agent)
status: in_progress
priority: 3
created: "2026-06-27"
updated: "2026-06-27"
depends_on: []
spec_refs: []
---

## Description
Make the reasoning (thinking/effort) level configurable **independently per role** — the
coordinator, implementer, and reviewers can each reason at a different depth (e.g.
coordinator `xhigh`, implementer `low`, reviewers `high`) even when they share a backend.

Today thinking is per-model (`[models.X]`) plus a single **session-wide** override
(`SetThinking(level)` → `Session.thinkLevel string`) that hits every agent at once. There is
no way to differentiate roles that map to the same model. This task adds a per-role layer in
config and in the live settings overlay.

### Design (spec §7.4, §12, §13, §18.2)

Resolution precedence for an agent (highest wins):
1. per-role session override (settings overlay / `SetThinking`),
2. per-role config (`[roles.thinking]`),
3. per-model config (`[models.X]` thinking/effort/display) — existing behaviour as fallback,
4. package defaults (adaptive / high / summarized).

A single-knob level (`off | low | medium | high | xhigh | max`) maps to adaptive thinking at
that effort with summarized display; `off` disables reasoning. `reviewers` carries one level
applied uniformly to the whole reviewer fan-out.

### Work

**Config (`internal/config`).** Add an optional `[roles.thinking]` sub-table mapping role →
level (`Roles.Thinking struct{ Coordinator, Implementer, Reviewers string }` with toml tags,
or a `map[string]string`). Validate levels against the allowed set. Expose a registry helper
to read the per-role override (e.g. `RoleThinking(role string) (level string, ok bool)`).
`Save`/`DefaultAnthropic` round-trip the new field.

**Session (`internal/session/session.go`).**
- Replace `thinkLevel string` with a per-role map (`map[string]string` keyed by
  `coordinator|implementer|reviewers`).
- Make thinking resolution **role-aware**: change `thinkingFor` (and its call sites that
  build the coordinator loop, `agentSpec`, and the manager's `agentSpec`) to resolve by
  *role* applying the precedence above — per-role session override → per-role config →
  per-model config → defaults. Note the coordinator/implementer/reviewer call sites already
  know which role they are building.
- `SetThinking(role, level)`: empty `role` sets all three roles (back-compat / "all");
  a specific role sets just that one. Rebuild the implementer/reviewer specs and update the
  live coordinator loop only as relevant to the changed role(s). Emit
  `thinking_level_changed` with the role included.

**Proto/RPC (`proto/ycc/v1/ycc.proto`, `internal/server`).** Add `string role = 3` to
`SetThinkingRequest` (empty = all roles, back-compatible). Regenerate; thread `role` through
the server handler to `Session.SetThinking`.

**TUI (`internal/tui`).** Replace the single thinking overlay row with per-role thinking
selection (coordinator / implementer / reviewers rows, each cycling the level), issuing
`SetThinking(role, level)`. Render the per-role `thinking_level_changed` event. Track per-role
levels in model state (replace `thinkLevel string`).

### Acceptance criteria
- [ ] `[roles.thinking]` config (per-role levels) parses, validates, and round-trips through
      `config.Save`/`Load`; an unset role falls back to per-model config then defaults.
- [ ] An agent's reasoning is resolved with the documented precedence (per-role override >
      per-role config > per-model config > defaults), verified by a unit test that gives two
      roles the same model but different thinking and asserts each agent's resolved settings.
- [ ] `SetThinkingRequest` carries a `role`; empty role updates all roles, a specific role
      updates just that one; the change is recorded in `thinking_level_changed` with the role.
- [ ] Setting the coordinator's thinking mid-session updates the live coordinator loop;
      setting implementer/reviewer thinking affects the next spawn (existing behaviour, now
      per-role).
- [ ] TUI settings overlay exposes per-role thinking pickers and issues per-role `SetThinking`.
- [ ] `go build ./...` and `go test ./...` pass.

## Acceptance criteria

## Work log
- 2026-06-27 plan: Make reasoning configurable independently per role (coordinator/implementer/reviewers), layering a per-role override over the existing per-model config.  1. config: add optional `[roles.thinking]` (Co
…[truncated]
- 2026-06-27 plan: Make reasoning level configurable independently per role (coordinator/implementer/reviewers), layering a per-role override over existing per-model config.  1. config (internal/config/config.go):    - 
…[truncated]
- 2026-06-27 implementer report: Implemented per-role thinking levels (task 0036) with the documented precedence: per-role session override > per-role config (`[roles.thinking]`) > per-model config > package defaults.  **config (inte
…[truncated]
- 2026-06-27 review (claude): accept — The change correctly implements per-role thinking levels across all layers. Config adds an optional `[roles.thinking]` sub-table with validation and a `RoleThinking` registry helper that round-trips t
…[truncated]
