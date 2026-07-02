---
id: "0091"
title: Drop spec/feature/bug/backlog home-menu presets in favor of just the pm mode
status: todo
priority: 3
created: "2026-06-30"
updated: "2026-06-30"
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

## Work log
