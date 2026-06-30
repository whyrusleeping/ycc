---
id: "0076"
title: Move activity spinner to the bottom next to the session input box
status: todo
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
