---
id: "0091"
title: Drop spec/feature/bug/backlog home-menu presets in favor of just the pm mode
status: done
priority: 3
created: "2026-06-30"
updated: "2026-07-02"
depends_on: []
spec_refs: []
---

## Description
The home menu still lists the old `spec`/`feature`/`bug`/`backlog` framings as separate
opening-prompt presets (introduced in 0021's `Presets()` in `internal/orchestrator/modes.go`,
rendered by the TUI home menu). The user finds this redundant: these are all just `pm` work, so
the menu should drop those presets and present a single `pm` mode whose description makes clear
it covers planning, spec authoring, backlog grooming, new features, and bug intake.

Scope:
- Remove the `feature`/`bug`/`spec`/`backlog` presets from `Presets()`. **Confirmed with the
  user (2026-07-02): KEEP the `onboard` preset** — it is a distinct first-run flow (see 0024).
- Update the `pm` `ModeInfo` title/description in `Modes()` so it reads as the catch-all for
  spec/backlog/feature/bug planning work — "no implementation".
- Update the TUI home menu so the dropped presets no longer appear; ensure the menu still renders
  cleanly with `pm`/`chat`/`work` (+ onboard if retained).
- The preset opening-prompt strings (`featurePresetPrompt`, etc.) and the underlying `pm` mode
  capabilities are unaffected — this is a menu/labeling change only. Consider whether to keep the
  prompt constants for reference or remove the now-unused ones.

Note: this is a refinement of 0021, which collapsed the four modes into `pm` + presets; here we
go further and remove the presets from the menu entirely.

## Acceptance criteria
- [ ] the `spec`/`feature`/`bug`/`backlog` presets no longer appear in the home menu
- [ ] the `pm` mode's menu description clearly communicates it handles spec/backlog/feature/bug
      planning and intake (no implementation)
- [ ] home menu still lists `pm`/`chat`/`work` (and `onboard` if retained) and renders correctly
- [ ] any now-unused preset code/prompts are removed or intentionally retained; `go test ./...` green

## Acceptance criteria

## Plan

Drop the spec/feature/bug/backlog home-menu presets, keeping only `onboard`, and make `pm` read as the catch-all planning/intake mode.

1. internal/orchestrator/modes.go
   - `Presets()`: remove the `feature`, `bug`, `spec`, `backlog` entries; keep only `onboard` (confirmed with user to retain).
   - `Modes()`: update the `pm` ModeInfo description so it clearly covers spec authoring, backlog grooming, new-feature planning, and bug intake — "no implementation". e.g. "Plan and intake — spec authoring, backlog grooming, new features, bug reports. No implementation."
   - Update the doc comments on `Modes()` and `Preset`/`Presets()` that still describe the four framings as presets.
2. internal/orchestrator/prompts.go
   - Remove the now-unused `featurePresetPrompt`, `bugPresetPrompt`, `specPresetPrompt`, `backlogPresetPrompt` constants (keep `onboardPresetPrompt`). Adjust any comments referencing them.
3. internal/orchestrator/modes_test.go
   - Update `TestPresetsOpenPM` to expect exactly the `onboard` preset, and assert the dropped preset names no longer appear.
4. spec.md
   - Update §9 (the paragraph describing the four opening-prompt presets, ~lines 392–398) to say the former spec/feature/bug/backlog framings are now simply `pm` work (dropped as separate presets), with `onboard` the sole remaining preset. Keep the "typed prompt composes with a preset" rule (still applies to onboard).
   - Update §19.2 (~lines 1137–1170) where it says onboarding "slots in ... alongside the existing feature/bug/spec/backlog presets" and "Relation to existing presets" to reflect that those presets are gone and onboard is the only preset.
5. No TUI code change needed: the home menu renders modes+presets from the ListModes RPC, so dropped presets disappear automatically. Verify no TUI test references the dropped preset names.
6. Verify: `go build ./... && go test ./...` green.

### Starting points
- internal/orchestrator/modes.go: Modes() and Presets() (~lines 25–53)
- internal/orchestrator/prompts.go:234-260 — the preset prompt constants
- internal/orchestrator/modes_test.go: TestPresetsOpenPM (~line 46)
- spec.md §9 ~lines 392–398 and §19.2 ~lines 1134–1170 mention the presets
- TUI needs no change: internal/tui/tui.go modesMsg handler builds the menu from the RPC response

## Work log
- 2026-07-02 plan: Drop the spec/feature/bug/backlog home-menu presets, keeping only `onboard`, and make `pm` read as the catch-all planning/intake mode.  1. internal/orchestrator/modes.go    - `Presets()`: remove the `
…[truncated]
- 2026-07-02 context hints: 5 recorded with plan
- 2026-07-02 context hints: internal/orchestrator/modes.go: Modes() and Presets() (~lines 25–53); internal/orchestrator/prompts.go:234-260 — the preset prompt constants; internal/orchestrator/modes_test.go: TestPresetsOpenPM
…[truncated]
- 2026-07-02 implementer report: Dropped the spec/feature/bug/backlog home-menu presets in favor of just the pm mode, keeping onboard.  Changes: - internal/orchestrator/modes.go:   - `Presets()` now returns only the `onboard` preset 
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The change correctly drops the spec/feature/bug/backlog home-menu presets while retaining onboard (as confirmed with the user). Presets() now returns only onboard, the pm ModeInfo description is updat
…[truncated]
- 2026-07-02 decision: accept — commit: orchestrator: drop spec/feature/bug/backlog home-menu presets — pm is the catch-all planning/intake mode; onboard remains the sole preset (0091)
- 2026-07-02 usage: 12,441 tok (in 70, out 12,371, cache_r 650,532, cache_w 62,338) · cost n/a (unpriced)
  implementer: 6,244 tok (in 28, out 6,216, cache_r 302,622, cache_w 29,433) · cost n/a (unpriced)
  coordinator: 4,891 tok (in 28, out 4,863, cache_r 303,079, cache_w 23,577) · cost n/a (unpriced)
  reviewer:Claude: 1,306 tok (in 14, out 1,292, cache_r 44,831, cache_w 9,328) · cost n/a (unpriced)
