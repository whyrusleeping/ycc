---
id: "0289"
title: Thinking levels should be configured per model not globally per role
status: done
priority: 3
created: "2026-08-07"
updated: "2026-08-08"
depends_on: []
spec_refs: []
---

## Description

## Acceptance criteria

## Plan

Goal: thinking levels attach to MODELS, not roles. Today resolution is: per-role session override → `[roles.thinking]` config → per-model `[models.X]` config → defaults, and SetThinking persists to roles.thinking.*. After this change the role layer is gone: resolution is per-model session override → per-model config → defaults, and setting a level via the settings UI persists into that model's `[models.X]` entry. Swapping a role to another model then picks up that model's own thinking level.

Design decisions (wire-compatible; no proto shape change):
- SetThinkingRequest keeps `{session_id, role, level}`. The daemon maps role → the model(s) currently assigned to that role (coordinator model, implementer model, ALL reviewer models; empty role = all three roles' models, deduped) and applies/persists the level per MODEL.
- GetSettings keeps coordinator_thinking/implementer_thinking/reviewers_thinking; values become the resolved level of each role's current model (reviewers: first reviewer's model). TUI/iOS clients keep working unchanged.
- Old `[roles.thinking]` tables in existing ycc.toml are silently ignored (go-toml/v2 ignores unknown keys); documented in spec.

Changes:
1. internal/config/config.go:
   - Remove RoleThinking struct, Roles.Thinking field, RoleThinking(), SetRoleThinking(), RoleThinkingLevels(), and the roles.thinking validation block (~line 783-789).
   - Add Registry.SetModelThinking(name, level string) error: validate level (validThinkingLevel) and that the model exists; level "off" → m.Thinking="off"; else m.Thinking="adaptive", m.Effort=level (leave ThinkingDisplay alone); persist, revert on persist failure.
   - Add Registry.ModelThinkingLevel(name) string: single-knob level for a model from ResolveThinking ("off" when disabled, else effort) for seeding pickers.
2. internal/session/session.go:
   - s.thinkLevels: key by MODEL name instead of role. Session.SetThinking(role, level): compute affected model set from current role assignments (under s.mu), set overrides per model, rebuild implementer/reviewer specs and update the live coordinator loop as today (a role is affected if any of its models is in the set), emit ThinkingLevelChanged with {"role": role-or-"all", "models": [...], "from", "to"}, and persist via reg.SetModelThinking for each affected model (event-log failure checks unchanged).
   - Session.thinkingFor(role, name): drop the role layers → session override for model name, else reg.ThinkingFor(name). Keep the role param only if still needed by callers; otherwise simplify signature.
   - Manager.thinkingFor: just reg.ThinkingFor(name). Manager.SetRoleThinking → replace with a role→models resolution + SetModelThinking (used by server when no live session). Manager.ThinkingLevels(): return ModelThinkingLevel of each role's configured model (reviewers: first reviewer; fall back to default effort when no reviewers).
3. internal/server/server.go: update SetThinking/GetSettings doc comments; behavior flows through Manager/Session changes.
4. internal/tui/settings.go: update comments/help text that say "per-role" (behavior is unchanged client-side: +/- on a role row sets the level for that role's current model(s)). Check internal/tui/transcript.go thinking_level_changed rendering still works with the payload (role kept).
5. proto/ycc/v1/ycc.proto: update the SetThinking/GetSettings comments (lines ~267-271, ~348-359) to describe per-model semantics. Regenerate: Go via buf (local plugins); Swift regen uses remote BSR plugins — attempt it; if the network/plugin fetch fails, revert the .proto comment edits instead of leaving generated-code drift.
6. spec.md: rewrite §13 "Per-role reasoning" block (~lines 1195-1211) to per-model semantics with the new 3-level precedence; drop `[roles.thinking]` from the config sample (~1059-1062); update §18.2 SetThinkingRequest description (~975-985) and the settings-overlay Thinking bullet (~1548-1560) — the role in the request now names WHICH role's model(s) to change, persistence goes to `[models.X]`, and old `[roles.thinking]` tables are ignored. Grep spec/docs for other `roles.thinking` mentions.
7. Tests: update internal/config/config_test.go (SetRoleThinking → SetModelThinking + persistence round-trip), internal/session/settings_test.go and settings_persistence_failure_test.go (override now keyed per model; model-swap picks up the new model's level — add a case asserting that), internal/server/server_test.go, internal/tui/settings_test.go as needed.

Verification: go build ./... && go test ./internal/config ./internal/session ./internal/server ./internal/tui ./internal/engine; run `go run ./cmd/ycc spec-check` (note: known flaky tests in session/setup/tools per memory — compare against HEAD if something unrelated fails).

### Starting points
- internal/config/config.go: RoleThinking/SetRoleThinking/RoleThinkingLevels (~1160-1230), validation ~783-789, ResolveThinking ~212, validThinkingLevel ~264
- internal/session/session.go: SetThinking ~834-913, thinkingFor ~932-947, agentSpec ~950, Manager.thinkingFor ~2410, Manager.ThinkingLevels ~2635, Manager.SetRoleThinking ~2623
- internal/server/server.go: GetSettings ~466, SetThinking ~684
- internal/tui/settings.go: setThinking ~21, overlayAdjustThinking ~197
- spec.md: §13 per-role reasoning ~1195-1211, config sample ~1044-1062, §18.2 ~975-985 and ~1548-1560
- proto/ycc/v1/ycc.proto lines 267-271 and 348-359 (comments only)
- buf is in ~/go/bin; Swift proto regen uses remote BSR plugins (network); Go regen local

## Work log
- 2026-08-08 plan: Goal: thinking levels attach to MODELS, not roles. Today resolution is: per-role session override → `[roles.thinking]` config → per-model `[models.X]` config → defaults, and SetThinking persists
…[truncated]
- 2026-08-08 context hints: 7 recorded with plan
- 2026-08-08 context hints: internal/config/config.go: RoleThinking ~232-253, validation ~783-789, RoleThinking/SetRoleThinking/RoleThinkingLevels ~1160-1230, ResolveThinking ~212, validThinkingLevel ~264, ThinkingFor ~1478; int
…[truncated]
- 2026-08-08 preload: 3 file(s), ~11 KiB seeded into implementer context
- 2026-08-08 implementer report: Implemented task 0289: removed `[roles.thinking]` config/state and changed thinking resolution, live overrides, persistence, manager/server role mapping, and settings seed values to be model-based. `S
…[truncated]
- 2026-08-08 review tier: single-opus — reviewers: sol
- 2026-08-08 review (sol): accept — The implementation satisfies task 0289: role-level config/state is removed, model-level resolution and persistence are implemented with validation and rollback, live-session role-to-model mapping dedu
…[truncated]
