---
id: "0217"
title: Expose work.implementation (delegate/direct) in the settings overlay
status: done
priority: 3
created: "2026-07-16"
updated: "2026-08-08"
depends_on: []
spec_refs:
    - The `work` orchestration (in detail)
    - Client UI (TUI)
---

## Description
The `work.implementation` config setting (spec §10, `delegate` vs `direct`) is currently only
configurable via `ycc.toml` and resolves at session start. Expose it as a live, persisted
setting from the settings overlay (§18.2), matching the pattern of `SetInteractionLevel` /
`SetThinking` / `SetRoleConfig`.

Scope:
- Add a `SetWorkImplementation` RPC (proto + regen) and server handler that calls
  `config.Registry.SetWorkImplementation` (already implemented — persists to ycc.toml).
- Surface the current value via `ListModels`/settings seed so the overlay shows the real state.
- Add an overlay row (a two-choice toggle: delegate | direct).
- Mid-session application is the tricky part: changing the strategy changes the work
  coordinator's TOOLSET and system prompt, so the live coordinator loop must be rebuilt
  (unlike SetThinking which only tweaks reasoning). Either rebuild the loop at the next safe
  checkpoint, or document that the change applies to the next session only. Decide and
  implement; update spec §10 to drop the "follow-up" note once done.

## Acceptance criteria
- [x] `SetWorkImplementation` RPC exists and persists the choice to ycc.toml.
- [x] Settings overlay shows and can change delegate/direct.
- [x] Mid-session semantics are implemented and documented (rebuild-now or next-session).
- [x] spec §10 updated to reflect the shipped behaviour.

## Plan

Goal: expose work.implementation (delegate|direct) as a live, persisted setting in the TUI settings overlay, per spec §10/§18.2.

Decision on mid-session semantics: NEXT-SESSION-ONLY. The strategy is resolved at session start (internal/session/session.go:2057 threads reg.WorkImplementation() into orchestrator.Deps) and changing it mid-session would require rebuilding the coordinator loop's toolset+system prompt. We persist the new choice immediately; it takes effect for the next session. Document this in spec §10 (replace the "follow-up" note) and annotate the overlay row.

Steps:
1. proto/ycc/v1/ycc.proto:
   - Add `SetWorkImplementation(SetWorkImplementationRequest) returns (SetWorkImplementationResponse)` to the Ycc service; request has `string implementation = 1` ("delegate"|"direct"); empty response. Comment: persists to ycc.toml, applies to the next session.
   - Add `string work_implementation = 8;` to ListModelsResponse (resolved value, so the overlay seeds with the real state).
   - Regen Go: `buf generate` (local plugins). Optionally attempt Swift regen with `buf generate --template buf.gen.swift.yaml` (remote BSR plugins, needs network); if it fails, skip it and say so in the report.
2. internal/session/session.go (Manager accessors, near SetRoles/Roles):
   - `func (m *Manager) WorkImplementation() string { return m.reg.WorkImplementation() }`
   - `func (m *Manager) SetWorkImplementation(impl string) error { return m.reg.SetWorkImplementation(impl) }` (Registry method already exists in internal/config/config.go and validates/persists).
3. internal/server/server.go:
   - ListModels: set `WorkImplementation: s.mgr.WorkImplementation()` in the response.
   - New handler SetWorkImplementation: validate via mgr.SetWorkImplementation, map error to CodeInvalidArgument; comment that it persists the default and applies to the next session (no live-session rebuild).
4. internal/tui:
   - msgs.go/menu.go: carry `workImpl` through modelsMsg / fetchModels.
   - tui.go modelsMsg case: seed a new model field `m.workImpl` (add field to model struct, likely in tui.go near roleCoord).
   - settings.go: add row `ovWorkImpl` (place after ovBackends, before ovTheme). left/right (and enter) toggles delegate↔direct: update m.workImpl and issue a cmd calling the SetWorkImplementation RPC (pattern of setThinking). Row label "work implementation", value e.g. `delegate (next session)`. Update overlayView rows list in matching order.
5. Tests:
   - internal/server/server_test.go: SetWorkImplementation round-trip — call RPC, assert ListModels reports the new value and it persisted (follow the pattern of existing settings-persistence tests); invalid value → InvalidArgument.
   - internal/tui/settings_test.go: overlay toggle test — cursor to ovWorkImpl, left/right flips the value (follow existing overlay tests).
6. spec.md §10 ("Implementation strategy — delegate vs. direct"): replace the "Exposing it ... is a follow-up" sentence with the shipped behaviour: SetWorkImplementation RPC, overlay row, persisted to ycc.toml, next-session semantics. Mention the row in §18.2's overlay description if it enumerates rows.
7. Tick the acceptance criteria in backlog/0217-*.md.
8. Verify: `go build ./... && go vet ./... && go test ./internal/server/... ./internal/tui/... ./internal/config/...`.

IMPORTANT: the working tree contains OTHER tasks' uncommitted in_review work (server.go, proto files, spec.md, iOS files are already dirty). Do NOT revert, reformat, or "clean up" any pre-existing modifications — add only your own changes on top. Coordinator will commit selectively.

### Starting points
- proto/ycc/v1/ycc.proto: ListModelsResponse (~line 259), SetThinking RPC pattern (~345, 795)
- internal/server/server.go: ListModels ~450, SetThinking/SetRoleConfig handlers ~664-694
- internal/session/session.go: Manager accessors SetRoles/Roles/ThinkingLevels ~2605-2630; WorkImplementation threading at ~2057
- internal/config/config.go: Registry.WorkImplementation ~996, SetWorkImplementation ~1011 (already implemented)
- internal/tui/settings.go: overlay row consts ov* ~60, overlayAdjust, overlayView rows slice
- internal/tui/menu.go fetchModels ~28; internal/tui/msgs.go modelsMsg ~25; internal/tui/tui.go 'case modelsMsg:' ~773
- spec.md ~839-860: work.implementation paragraph with the follow-up note to replace
- regen: buf in ~/go/bin; Go plugins local (buf generate); swift template buf.gen.swift.yaml uses remote plugins (network)

## Work log
- 2026-08-08 plan: Goal: expose work.implementation (delegate|direct) as a live, persisted setting in the TUI settings overlay, per spec §10/§18.2.  Decision on mid-session semantics: NEXT-SESSION-ONLY. The strategy i
…[truncated]
- 2026-08-08 context hints: 8 recorded with plan
- 2026-08-08 context hints: Pattern to mirror: SetThinking RPC (proto ~345/795, server.go ~683, tui settings.go setThinking ~21); config.Registry.SetWorkImplementation already validates delegate|direct and persists to ycc.toml �
…[truncated]
- 2026-08-08 preload: 4 file(s), ~28 KiB seeded into implementer context
- 2026-08-08 implementer report: Implemented task 0217.  Changes: - Added `SetWorkImplementation` and `ListModelsResponse.work_implementation` to the protobuf API; regenerated Go and Swift protobuf/Connect clients. - Added Manager/se
…[truncated]
- 2026-08-08 review tier: single-opus — reviewers: sol
- 2026-08-08 review (sol): accept — The change fully implements task 0217. The protobuf API and generated Go/Swift bindings expose the resolved setting and setter RPC; the server delegates validation/persistence to the registry and repo
…[truncated]
