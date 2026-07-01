---
id: "0105"
title: Interrupt keybinding that works without kitty keyboard protocol (ctrl+i == tab)
status: todo
priority: 3
created: "2026-07-01"
updated: "2026-07-01"
depends_on: []
spec_refs:
    - 18.7 Interrupt & steer (pause / correct / resume)
---

## Description
## Description
`ctrl+i` is byte-identical to Tab (0x09); bubbletea v2 only disambiguates on terminals supporting the kitty keyboard enhancement. On plain xterm/ssh/tmux setups the primary runtime affordance — interrupt — is unreachable from the keyboard (the settings-overlay "Interrupt agent" row now exists as the fallback, but a direct key should work everywhere).

Add a binding that survives everywhere (candidates: `ctrl+x`, `ctrl+g`, or double-esc), keep `ctrl+i` where distinguishable, and render the session-footer hint from the binding that actually works (bubbletea delivers `KeyboardEnhancementsMsg` when disambiguation is available — use it to pick the shown hint).

## Acceptance criteria
- [ ] Interrupt reachable via a single key chord on terminals without the kitty protocol
- [ ] Footer help shows the effective binding (not a dead one)
- [ ] `ctrl+i` still works where the terminal disambiguates
- [ ] No collision with textarea editing keys in the session input

## Acceptance criteria

## Work log
