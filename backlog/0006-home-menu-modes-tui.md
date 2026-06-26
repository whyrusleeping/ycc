---
id: "0006"
title: Home menu, spec/backlog/feature/bug modes, TUI (M4)
status: done
priority: 4
created: 2026-06-25
updated: 2026-06-25
depends_on: ["0005"]
spec_refs: ["Modes", "System architecture"]
---

## Description
Round out the product surface: the home menu (ListModes + StartSession), the remaining
coordinator modes (spec authoring, backlog building, feature/bug intake that proposes a
plan and updates spec+backlog then optionally flows into work), and a Bubble Tea TUI as
the primary local client.

## Acceptance criteria
- [ ] ListModes + home menu in the TUI
- [ ] spec mode (section-wise update_spec) and backlog mode (create/update tasks)
- [ ] feature/bug mode: understand → propose_plan → on accept update spec+backlog
- [ ] mode_changed transition from feature/bug into work within one session
- [ ] TUI renders event stream, subagent drill-down, ask_user prompts

## Work log
- 2026-06-26 implemented:
  - `internal/docs/spec.go`: section-scoped spec.md editing (ReadSpec, UpdateSpecSection
    replace/append, SpecSections). Tested.
  - `internal/orchestrator/modes.go`: `Modes()` (work/spec/backlog/feature/bug),
    `BuildMode(mode, deps, level)` assembling per-mode tool registry + system prompt; new
    tools read_spec/update_spec/create_task and `switch_to_work` control tool. Mode-specific
    prompts in prompts.go.
  - engine: `tools.Control.Mode` + `engine.Result.NextMode` so a control tool can request a
    mode transition; `tools.Inspect` (read+bash subset, factored out of Reviewer).
  - `internal/session`: generalized to build any mode via a `buildLoop` closure; run() loop
    handles `NextMode` → emits `mode_changed`, rebuilds the loop, continues IN THE SAME
    session. Removed the old worker fallback.
  - proto/server: `ListModes` RPC. event type `mode_changed`.
  - `internal/tui`: Bubble Tea home menu (ListModes + prompt) + session view (live event
    stream via Subscribe, subagent indentation + per-actor color, inline ask_user prompt,
    input box → SendInput). `cmd/ycc` launches the TUI with no subcommand; added `ycc modes`.
  - Tests: spec section edit, modes toolset/switch_to_work/create_task/update_spec. All green.
- 2026-06-26 verified LIVE: `ycc modes` returns all 5 modes (ListModes RPC). A `feature`-mode
  autonomous session explored the codebase, created task 0001, called switch_to_work →
  `mode_changed` → the SAME session continued as a work coordinator and implemented/reviewed/
  committed (3bbe0f2) the Subtract function. Prompt ordering fixed (create_task before
  propose_plan) after observing a self-corrected hiccup.
- NOT verified here: the interactive TUI rendering — Bubble Tea needs a real TTY (fails
  gracefully headless). Builds clean; all RPCs it uses are verified via the CLI. Needs a
  human terminal drive. ChooseMode RPC not implemented (transitions are agent-driven via
  switch_to_work + StartSession from the menu); candidate follow-up if client-driven mode
  switching is wanted.