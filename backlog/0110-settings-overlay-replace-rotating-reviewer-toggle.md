---
id: "0110"
title: 'Settings overlay: replace rotating reviewer toggle with an explicit multi-select'
status: todo
priority: 4
created: "2026-07-01"
updated: "2026-07-01"
depends_on: []
spec_refs:
    - 18.2 Settings overlay
---

## Description
## Description
`toggleReviewer` flips membership of a *hidden rotating sub-index*: each space toggles "whichever model the invisible cursor is on", then advances it. With several configured models the effect of a keypress is unpredictable — the user can't see which model space will toggle next.

Replace with an explicit affordance: either a drill-in checkbox list (reuse the shared browser/modal component: ↑/↓ move, space toggles the highlighted model, esc back), or an inline sub-cursor on the reviewers row (←/→ moves a visible highlight across the `[x] name` chips, space toggles the highlighted one).

**User decision (2026-07-02): implementer's choice between the two shapes — pick whichever fits the existing overlay code best; the hard requirement is only that the next toggle's target is always visible.**

## Acceptance criteria
- [ ] The model affected by the next toggle is always visible before pressing the key
- [ ] Non-empty reviewer invariant preserved; changes still apply+persist immediately via SetRoleConfig
- [ ] Overlay help text updated

## Acceptance criteria

## Work log
