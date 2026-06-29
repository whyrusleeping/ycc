---
id: "0049"
title: Show agent action log in quick-add task overlay after submit
status: done
priority: 3
created: "2026-06-27"
updated: "2026-06-29"
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
- 2026-06-29 plan: Make the quick-add (Ctrl+N) capture overlay stream the capture agent's action log live, instead of showing nothing until completion. The capture agent already runs server-side via an Emitter whose Rec
…[truncated]
- 2026-06-29 implementer report: Implemented task 0049: the Ctrl+N quick-add capture overlay now streams the capture agent's action log live instead of showing nothing until completion.  Changes: - proto/ycc/v1/ycc.proto: `CaptureBac
…[truncated]
- 2026-06-29 review tier: single-opus — reviewers: Claude
- 2026-06-29 review (Claude): accept — The change fully satisfies the task. The quick-add (Ctrl+N) capture overlay now streams the capture agent's action log live: CaptureBacklogItem was converted to a server-streaming RPC, a new event.Fun
…[truncated]
- 2026-06-29 decision: accept — commit: tui: stream capture agent action log live in quick-add overlay [0049]  Convert CaptureBacklogItem to a server-streaming RPC and thread the capture agent's events through a new event.FuncRecorder so th
…[truncated]
