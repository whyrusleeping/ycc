---
id: "0105"
title: Interrupt keybinding that works without kitty keyboard protocol (ctrl+i == tab)
status: done
priority: 3
created: "2026-07-01"
updated: "2026-07-02"
depends_on: []
spec_refs:
    - 18.7 Interrupt & steer (pause / correct / resume)
---

## Description
## Description
`ctrl+i` is byte-identical to Tab (0x09); bubbletea v2 only disambiguates on terminals supporting the kitty keyboard enhancement. On plain xterm/ssh/tmux setups the primary runtime affordance — interrupt — is unreachable from the keyboard (the settings-overlay "Interrupt agent" row now exists as the fallback, but a direct key should work everywhere).

Add a binding that survives everywhere, keep `ctrl+i` where distinguishable, and render the session-footer hint from the binding that actually works (bubbletea delivers `KeyboardEnhancementsMsg` when disambiguation is available — use it to pick the shown hint).

**Decided (user, 2026-07-02): the fallback chord is `ctrl+x`.** (Candidates considered: ctrl+x, ctrl+g, double-esc.)

## Acceptance criteria
- [ ] Interrupt reachable via a single key chord on terminals without the kitty protocol
- [ ] Footer help shows the effective binding (not a dead one)
- [ ] `ctrl+i` still works where the terminal disambiguates
- [ ] No collision with textarea editing keys in the session input

## Acceptance criteria

## Plan

Goal: make interrupt reachable everywhere via `ctrl+x` (decided fallback), keep `ctrl+i` where the terminal disambiguates it from Tab, and render the session-footer hint from the binding that actually works.

Implementation (all in internal/tui/tui.go):
1. Add a model field, e.g. `keyEnhanced bool` — true once the terminal reports kitty keyboard-enhancement support.
2. In the top-level `Update` (before state dispatch, near the MouseMsg/KeyMsg pre-handling), handle `tea.KeyboardEnhancementsMsg`: set `m.keyEnhanced = msg.SupportsKeyDisambiguation()`. Bubble Tea v2 requests basic disambiguation by default and delivers this msg automatically when supported — no View changes needed.
3. In `updateSession`'s KeyMsg switch (the non-picking switch around line 2352), make `"ctrl+x"` trigger the same interrupt path as `"ctrl+i"` (combine cases: `case "ctrl+i", "ctrl+x":`). `ctrl+x` (0x18) is a distinct control byte delivered on every terminal, and the bubbles textarea default keymap does not use it (verified: it uses ctrl+f/b/n/p/w/k/u/m/h/d/a/e/v/t), so no collision with session-input editing.
4. Add a small helper, e.g. `func (m model) interruptKeyHint() string { if m.keyEnhanced { return "ctrl+i" } return "ctrl+x" }`, with a comment explaining ctrl+i==tab on non-kitty terminals.
5. Use the helper in the three session footer help strings (~lines 5390–5397) so the hint reads `<key> interrupt` with the effective binding instead of a hard-coded `ctrl+i`.
6. Optionally update the comment on the settings-overlay ovInterrupt path (~3652) to mention ctrl+x as the universal chord (keep the overlay row as-is).

Tests:
- Unit test: model with `keyEnhanced` false → footer/help hint contains "ctrl+x interrupt"; after handling a `tea.KeyboardEnhancementsMsg` with a non-zero flag, hint shows "ctrl+i interrupt".
- Unit test (if session key dispatch is testable like existing tests do): a "ctrl+x" KeyPressMsg while running triggers the interrupt path (or at minimum doesn't fall through to the textarea).

Verify: go build ./... && go test ./internal/tui/... (and full go test ./... at the end).

### Starting points
- internal/tui/tui.go:2352-2370 — session KeyMsg switch with the existing ctrl+i case
- internal/tui/tui.go:1542-1553 — top-level Update pre-dispatch, good spot for tea.KeyboardEnhancementsMsg
- internal/tui/tui.go:5390-5398 — three footer help strings hard-coding 'ctrl+i interrupt'
- tea.KeyboardEnhancementsMsg has SupportsKeyDisambiguation() (Flags > 0); bubbletea v2.0.7 requests basic disambiguation by default
- bubbles/v2 textarea default keymap uses ctrl+f/b/n/p/w/k/u/m/h/d/a/e/v/t — ctrl+x is free

## Work log
- 2026-07-02 plan: Goal: make interrupt reachable everywhere via `ctrl+x` (decided fallback), keep `ctrl+i` where the terminal disambiguates it from Tab, and render the session-footer hint from the binding that actually
…[truncated]
- 2026-07-02 context hints: 5 recorded with plan
- 2026-07-02 context hints: internal/tui/tui.go:2352-2370 — session KeyMsg switch with the existing ctrl+i case; internal/tui/tui.go:1542-1553 — top-level Update pre-dispatch, good spot for tea.KeyboardEnhancementsMsg; inter
…[truncated]
- 2026-07-02 implementer report: Implemented the ctrl+x interrupt fallback and effective-binding footer hint. All in internal/tui.  Changes (internal/tui/tui.go): 1. Added `keyEnhanced bool` field to the model, documented as true onc
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The implementation satisfies all four acceptance criteria. ctrl+x is added as the universal interrupt chord (combined into the existing ctrl+i case in updateSession), and does not collide with any bub
…[truncated]
- 2026-07-02 decision: accept — commit: tui: add ctrl+x interrupt fallback + effective-binding footer hint (0105)
- 2026-07-02 usage: 21,381 tok (in 154, out 21,227, cache_r 1,320,930, cache_w 67,458) · cost n/a (unpriced)
  implementer: 11,835 tok (in 86, out 11,749, cache_r 773,770, cache_w 28,700) · cost n/a (unpriced)
  coordinator: 6,972 tok (in 44, out 6,928, cache_r 450,332, cache_w 26,486) · cost n/a (unpriced)
  reviewer:Claude: 2,574 tok (in 24, out 2,550, cache_r 96,828, cache_w 12,272) · cost n/a (unpriced)
