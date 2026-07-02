---
id: "0111"
title: TUI help modal (?) listing keybindings per state
status: done
priority: 4
created: "2026-07-01"
updated: "2026-07-02"
depends_on: []
spec_refs:
    - 18. Client UI (TUI)
---

## Description
## Description
The footer is the only key documentation and it's width-clamped — hints drop exactly when the terminal is narrow. The binding count (per-state: menu, session, browsers, pickers, overlays) has outgrown a one-line footer. Add a help modal on `?` (when the focused input is empty; always on ctrl+h/ctrl+_) listing bindings grouped by context, using the shared modal-card surface.

## Acceptance criteria
- [ ] `?`/ctrl+h opens a help modal over menu and session; esc closes
- [ ] Bindings grouped by state (home, session, question picker, browsers, overlays) and stay in sync with reality (single source of truth preferred over a hand-copied list)
- [ ] `?` still types into a non-empty input
- [ ] Footer mentions the help key

## Acceptance criteria

## Plan

Add a help modal to the TUI (task 0111), following the existing modal-overlay pattern in internal/tui/tui.go (flag on model + early dispatch in Update + early return in render + modalCard surface).

1. New file internal/tui/help.go — the single source of truth for the binding catalog:
   - types `helpBind{keys, desc string}` and `helpSection{title string; binds []helpBind}`.
   - `func (m *model) helpSections() []helpSection` returning bindings grouped by context: global (quit, settings, capture, backlog, browse selector, help itself), home menu, session (incl. paused/steer, work-loop shift+tab, interrupt chord via m.interruptKeyHint()), question picker, session browser (history), backlog browser, workstreams panel, plans/cost/digest browsers, capture overlay, settings overlay, model backends. Curated in ONE place with a prominent header comment: any binding added/changed in an update* function must be reflected here (grep anchor comments referencing the update functions).
   - `openHelp()`, `updateHelp(msg)`, `helpView()` on model. helpView renders via m.modalCard(" help — keybindings ", …, hint) with a scroll window (field `helpScroll int`): content lines beyond the height budget are windowed like browserCard does; up/down/pgup/pgdn scroll; esc (also q, ?) closes; ctrl+c → confirmQuit.
2. Model: add fields `helpOpen bool`, `helpScroll int` near the browse fields.
3. Dispatch: in Update, after the stateHistory branch and before the capture branch, `if m.helpOpen { return m.updateHelp(msg) }`. In render(), return m.helpView() first among the modal views.
4. Openers:
   - updateMenu: `?` and `ctrl+h` open help ONLY when strings.TrimSpace(m.prompt.Value()) == "" (same guard pattern as the existing "w"/"s" cases — fall through to the textarea otherwise, so ctrl+h keeps deleting backward and `?` keeps typing); `ctrl+_` always opens.
   - updateSession (non-picking switch): same — `?`/`ctrl+h` gated on empty m.input, `ctrl+_` always.
   - updateSession picking branch: `?`, `ctrl+h`, `ctrl+_` all open help (no free-text input is focused there).
   - Rationale for gating ctrl+h on empty input: bubbletea v2 decodes a legacy BS byte (0x08) as "ctrl+h", and the textarea binds ctrl+h to delete-char-backward — intercepting it unconditionally would eat backspace on BS-sending terminals. ctrl+_ (0x1F) is the unconditional chord.
5. Footers mention help: prepend "? help · " to the menu footer and to the session footer variants in sessionView (footers are width-clamped from the right, so the mention goes near the front).
6. Tests (internal/tui/tui_test.go, follow the TestOverlayRendersAsCard construction pattern — WindowSizeMsg first):
   - `?` on menu with empty prompt opens the modal; view shows the title and several section headings; esc closes and returns to the menu.
   - `?` with non-empty prompt/input types a literal '?' and does not open the modal (menu and session).
   - ctrl+_ opens with a non-empty session input; `?` opens from the picking state.
   - menu footer and session footer contain "? help".
   - help view lines fit the terminal width (reuse the lipgloss.Width check pattern).
7. Verify: go build ./... && go test ./internal/tui/... (and go vet).

### Starting points
- internal/tui/tui.go:1834 Update() — modal dispatch chain (capture/backlog/plans/cost/ws/digest/browse/mbOpen/overlay) — insert helpOpen branch before capture
- internal/tui/tui.go:5769 render() — modal view chain, insert helpView first
- internal/tui/tui.go:2639 updateMenu — see 'w'/'s' cases for the empty-prompt guard pattern
- internal/tui/tui.go:2752 updateSession — picking branch ~2777, main key switch ~2832
- internal/tui/tui.go:5730 modalCard(title, content, hint)
- internal/tui/tui.go:3188 browserCard — windowing pattern for scrollable modal content
- internal/tui/tui.go:5890 menu footer string; sessionView footers at ~6257-6280; m.interruptKeyHint()
- internal/tui/tui_test.go:819 TestOverlayRendersAsCard — model construction pattern for modal tests

## Work log
- 2026-07-02 plan: Add a help modal to the TUI (task 0111), following the existing modal-overlay pattern in internal/tui/tui.go (flag on model + early dispatch in Update + early return in render + modalCard surface).  1
…[truncated]
- 2026-07-02 context hints: 8 recorded with plan
- 2026-07-02 context hints: internal/tui/tui.go:1834 Update() — modal dispatch chain; insert helpOpen branch before the m.capture branch (~line 2434); internal/tui/tui.go:5769 render() — modal view chain; return helpView fir
…[truncated]
- 2026-07-02 implementer report: Implemented Task 0111: a TUI keybinding help modal opened with `?` / ctrl+h / ctrl+_.  ## Changes - **internal/tui/help.go (new)** — single source of truth for the binding catalog:   - `helpBind`/`h
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The change adds a TUI keybinding help modal (task 0111) that fully satisfies the acceptance criteria. A new internal/tui/help.go holds a single curated binding catalog (grouped by context) with a prom
…[truncated]
- 2026-07-02 decision: accept — commit: tui: add keybinding help modal on ?/ctrl+h/ctrl+_ with per-state catalog (task 0111)
- 2026-07-02 usage: 41,424 tok (in 216, out 41,208, cache_r 3,776,057, cache_w 196,054) · cost n/a (unpriced)
  implementer: 21,820 tok (in 120, out 21,700, cache_r 2,421,982, cache_w 63,619) · cost n/a (unpriced)
  coordinator: 15,489 tok (in 68, out 15,421, cache_r 1,094,760, cache_w 105,404) · cost n/a (unpriced)
  reviewer:Claude: 4,115 tok (in 28, out 4,087, cache_r 259,315, cache_w 27,031) · cost n/a (unpriced)
