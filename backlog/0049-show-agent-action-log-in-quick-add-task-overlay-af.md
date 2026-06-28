---
id: "0049"
title: Show agent action log in quick-add task overlay after submit
status: todo
priority: 3
created: "2026-06-27"
updated: "2026-06-27"
depends_on: []
spec_refs: []
---

## Description
## Description
The "quick add task" overlay (opened via Ctrl+N) currently gives no feedback after the user hits Enter to submit — the overlay stays in a waiting state until the operation is "done". This feels unresponsive. The overlay should surface the agent action log (the same streaming feedback shown elsewhere) so the user can see progress while the task is being captured/processed.

## Acceptance criteria
- After submitting the quick-add overlay (Enter), the agent action log is displayed within/under the overlay instead of showing no feedback.
- The action log streams updates live as the agent works, rather than only revealing output once the operation completes.
- The overlay remains usable: it still completes/closes appropriately when the operation finishes (and surfaces errors if it fails).
- Behavior is consistent with how the action log is presented in other surfaces.

## Acceptance criteria

## Work log
