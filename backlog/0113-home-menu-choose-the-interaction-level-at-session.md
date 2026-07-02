---
id: "0113"
title: 'Home menu: choose the interaction level at session start'
status: done
priority: 4
created: "2026-07-01"
updated: "2026-07-02"
depends_on: []
spec_refs:
    - 11. Interaction levels
    - 9. Modes (the home menu)
---

## Description
## Description
Spec §1 promises "the human chooses their level of involvement per session", but the home menu offers no way to pick the interaction level at `StartSession` — every session starts `judgement` (loop forces `autonomous`) and changing it requires opening the settings overlay mid-session. Add a visible level selector on the home menu (e.g. ←/→ cycles interactive/judgement/autonomous next to the prompt, shown as a pill) passed into `StartSession`. Deliberately not persisted as a global default (a sticky `autonomous` would be a safety footgun — mirror §18.2's reasoning).

## Acceptance criteria
- [ ] Level visible and cyclable on the home menu; passed to StartSession
- [ ] Defaults to judgement each launch (not persisted)
- [ ] Loop start still forces autonomous regardless of the selector
- [ ] Menu footer documents the key

## Acceptance criteria

## Plan

Add an interaction-level selector to the TUI home menu (spec §9, §11):

1. Model state: add a new field `menuLevel string` to `model` (internal/tui/tui.go), initialized to "judgement" in the constructor. Keep it SEPARATE from the existing `m.level` (which tracks the *current session's* level and is mutated by session events) so a finished autonomous session can't leave the selector sticky on "autonomous". Not persisted to prefs — defaults to judgement each launch, mirroring §18.2's safety reasoning.

2. Key handling in `updateMenu` (~line 2835): on "left"/"right", when the prompt textarea is empty (`strings.TrimSpace(m.prompt.Value()) == ""` — same gating pattern as the "?" help key), cycle `m.menuLevel` through the existing `levels` slice ("interactive", "judgement", "autonomous") using the existing `cycle` helper (used by the settings overlay at line ~4583). When the prompt is non-empty, fall through to the textarea so ←/→ keep moving the cursor mid-edit.

3. Start wiring: in the "enter" handler (~line 2878), change `m.startSession(e.mode, prompt, "")` to pass `m.menuLevel`. The loop path (line ~1135, `m.startSession("work", "", "autonomous")`) stays hard-coded to "autonomous" regardless of the selector — do not touch it. The loop entry's description already says autonomous; optionally note nothing else.

4. Rendering in `menuView` (~line 6117): show the level as a small pill next to/above the framed prompt input, e.g. a line like `  level: ‹judgement›` rendered with typeStyle/dimStyle near the input (consistent with how the settings overlay renders the interaction-level row). Keep it visually quiet; highlight the value.

5. Footer/help: add `←/→ level` to the menu footer string (~line 6118) and add a `{"← / →", "cycle the interaction level for the next session (when the prompt is empty)"}` row to the "home menu" section in internal/tui/help.go.

6. Tests in internal/tui/tui_test.go: 
   - default is judgement on a fresh model;
   - left/right cycle the level (and wrap), only when the prompt is empty (typed prompt → arrows don't change it);
   - the enter path calls startSession with the selected level (follow existing test patterns for menu key handling — inspect how existing tests drive updateMenu / capture StartSession requests);
   - loop start still passes "autonomous" even when the selector says "interactive".

7. Verify: go build ./... && go test ./internal/tui/...

### Starting points
- internal/tui/tui.go:2835-2879 updateMenu key handling (up/down/tab/enter)
- internal/tui/tui.go:741 startSession(mode,prompt,level)
- internal/tui/tui.go:1135 loop path forces autonomous
- internal/tui/tui.go:4529 levels slice; ~4583 cycle(levels, m.level, d) usage in settings overlay
- internal/tui/tui.go:6067-6127 menuView (framedInput + footer string)
- internal/tui/tui.go:406-419 model constructor (level: "judgement")
- internal/tui/help.go:60-67 home menu help section
- internal/tui/tui_test.go for existing menu/update test patterns

## Work log
- 2026-07-02 plan: Add an interaction-level selector to the TUI home menu (spec §9, §11):  1. Model state: add a new field `menuLevel string` to `model` (internal/tui/tui.go), initialized to "judgement" in the constru
…[truncated]
- 2026-07-02 context hints: 8 recorded with plan
- 2026-07-02 context hints: internal/tui/tui.go:2835-2879 updateMenu key handling (up/down/tab/enter); internal/tui/tui.go:741 startSession(mode,prompt,level); internal/tui/tui.go:1135 loop path forces autonomous; internal/tui/t
…[truncated]
- 2026-07-02 implementer report: Implemented a home-menu interaction-level selector (Task 0113).  Changes (internal/tui): - tui.go model struct: added `menuLevel string` field (interaction level for the NEXT session; not persisted). 
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The change correctly and completely implements the home-menu interaction-level selector. A separate non-persisted `menuLevel` field defaults to "judgement" each launch, ←/→ cycle it via the existi
…[truncated]
- 2026-07-02 decision: accept — commit: tui: add interaction-level selector (←/→) to the home menu (task 0113)
