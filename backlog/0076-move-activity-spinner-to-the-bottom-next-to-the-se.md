---
id: "0076"
title: Move activity spinner to the bottom next to the session input box
status: done
priority: 3
created: "2026-06-30"
updated: "2026-06-30"
depends_on: []
spec_refs: []
---

## Description
The activity spinner added in task 0062 currently animates in the session status bar at the top of the view. It should instead be shown at the **bottom of the session view, next to the input text box**, so the "agent is working" indicator sits with the place the user types.

### Scope
- Relocate the running-status spinner (`bubbles/spinner`) from the top status-bar header to render adjacent to the multiline input textarea at the bottom of the session view.
- Preserve existing behavior: the spinner animates only while `status == running` and stops on idle/paused/error (don't resurrect on a stale/latched error — see task 0051).
- Keep the single-physical-row layout invariants for both the header and the input area (no wrapping / frame overflow at narrow widths).

## Acceptance criteria
- [ ] The activity spinner appears at the bottom of the session view next to the input text box (no longer in the top status bar).
- [ ] Spinner animates while the session is `running` and stops on idle/paused/error.
- [ ] Input box + spinner layout does not wrap or overflow the frame at narrow terminal widths.
- [ ] TUI rendering covered by/consistent with existing status-bar / input tests.

## Acceptance criteria

## Work log
- 2026-06-30 plan: Relocate the activity spinner (bubbles/spinner) from the top status bar to the bottom session input area, next to the input textarea.  1. statusBar() (~line 3838-3856 in internal/tui/tui.go): remove t
…[truncated]
- 2026-06-30 implementer report: Moved the activity spinner from the top status bar to the bottom session input area (task 0076) in internal/tui/tui.go:  1. statusBar(): removed the running-only spinning-glyph branch; the header now 
…[truncated]
- 2026-06-30 review tier: single-opus — reviewers: Claude
- 2026-06-30 review (Claude): accept — The change relocates the activity spinner from the top status bar to a left-gutter column next to the bottom input textarea via the new model.inputRow(), used in all three session-view input cases. Ga
…[truncated]
- 2026-06-30 decision: accept — commit: tui: move activity spinner from status bar to bottom input row  Relocate the running-activity spinner next to the session input box: the header now always shows the static state-colored dot, and input
…[truncated]
