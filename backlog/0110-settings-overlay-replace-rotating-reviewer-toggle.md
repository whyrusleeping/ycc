---
id: "0110"
title: 'Settings overlay: replace rotating reviewer toggle with an explicit multi-select'
status: done
priority: 4
created: "2026-07-01"
updated: "2026-07-02"
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

## Plan

Implement the inline sub-cursor shape (fits the existing overlay code best — the reviewers row already renders `[x] name` chips via reviewerSummary and already has a sub-index field `reviewerSub`).

Changes in internal/tui/tui.go:
1. Make the sub-cursor explicit and visible:
   - In overlayAdjust (←/→), add `case ovReviewers:` that moves m.reviewerSub by d with wraparound over m.models (no toggle, no persist). Note overlayAdjust has a value receiver; either give it a pointer receiver path or return the mutated model (it already returns m, so mutating the value copy and returning it works — same pattern as other rows).
   - In toggleReviewer, toggle membership of m.models[m.reviewerSub] but STOP advancing the rotating index — the highlight stays put so the next toggle's target remains what the user sees.
   - Clamp m.reviewerSub into range whenever it's used/models change (defensive: if reviewerSub >= len(m.models) reset to 0) — models arrive async (msg handler ~line 1950) and can shrink via the backends modal.
2. Visibility: in reviewerSummary (or its call site in overlayView), when the overlay cursor is on ovReviewers, render the chip at reviewerSub highlighted (e.g. selStyle) so the model affected by the next space/enter is always visible before pressing the key. When the cursor is on another row, render chips undimmed/plain as today.
3. Persistence + invariant: keep the existing space-key path (toggle → if empty, restore first model → setRoleConfig("","",revs)). Fix the latent bug where enter (overlayActivate ovReviewers) toggles WITHOUT persisting: make enter go through the same toggle+guard+setRoleConfig path as space (factor a small helper, e.g. toggleReviewerAndPersist() (tea.Model, tea.Cmd), used by both).
4. Help text: update the overlay footer help — when the cursor is on the reviewers row show something like "←/→ highlight model · space/enter toggle · esc close"; the generic line otherwise drops "space toggle reviewer" wording or keeps a corrected generic hint. Keep it one line.
5. Update the comment on reviewerSub (no longer "rotating"; it's a visible sub-cursor).

Tests (internal/tui/tui_test.go, follow existing overlay test patterns):
- ←/→ on the reviewers row moves the sub-cursor with wraparound and does not change m.roleReviewrs.
- space toggles exactly the highlighted model; the highlight does not advance; the returned cmd persists via setRoleConfig (non-nil cmd) and the non-empty invariant restores a model when the last one is untoggled.
- enter on the reviewers row toggles AND returns a persist cmd (regression for the fixed bug).
- overlayView renders the highlighted chip distinctly when the cursor is on the reviewers row (e.g. contains a marker/style around the highlighted model name) so the next toggle target is visible.

Verify: go build ./... && go test ./internal/tui/... (plus go vet).

### Starting points
- internal/tui/tui.go:4281-4630 — settings overlay: updateOverlay, overlayAdjust, overlayActivate, toggleReviewer, overlayView, reviewerSummary
- internal/tui/tui.go:247-248 — roleReviewrs/reviewerSub fields
- internal/tui/tui.go:1462 setRoleConfig; ~1950 and ~5475 where models/reviewers arrive async
- existing overlay tests: internal/tui/tui_test.go ~815 (TestOverlayRendersAsCard), ~1286 (role cycling persistence)

## Work log
- 2026-07-02 plan: Implement the inline sub-cursor shape (fits the existing overlay code best — the reviewers row already renders `[x] name` chips via reviewerSummary and already has a sub-index field `reviewerSub`). 
…[truncated]
- 2026-07-02 context hints: 4 recorded with plan
- 2026-07-02 context hints: internal/tui/tui.go:4281-4630 — settings overlay: updateOverlay, overlayAdjust, overlayActivate, toggleReviewer, overlayView, reviewerSummary; internal/tui/tui.go:247-248 — roleReviewrs/reviewerSu
…[truncated]
- 2026-07-02 implementer report: Implemented the inline sub-cursor shape for the reviewers row in the settings overlay (Task 0110), replacing the hidden rotating toggle.  ## Changes (internal/tui/tui.go) 1. **Field comment (line 248)
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The change replaces the hidden rotating reviewer toggle with an explicit, visible inline sub-cursor exactly as planned. ←/→ (overlayAdjust ovReviewers) moves m.reviewerSub with wraparound and no s
…[truncated]
- 2026-07-02 decision: accept — commit: tui: replace hidden rotating reviewer toggle with visible inline sub-cursor (task 0110)
- 2026-07-02 usage: 18,582 tok (in 112, out 18,470, cache_r 1,135,666, cache_w 61,397) · cost n/a (unpriced)
  implementer: 13,398 tok (in 74, out 13,324, cache_r 846,128, cache_w 34,747) · cost n/a (unpriced)
  reviewer:Claude: 2,818 tok (in 24, out 2,794, cache_r 102,959, cache_w 14,237) · cost n/a (unpriced)
  coordinator: 2,366 tok (in 14, out 2,352, cache_r 186,579, cache_w 12,413) · cost n/a (unpriced)
