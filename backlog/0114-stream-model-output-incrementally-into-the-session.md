---
id: "0114"
title: Stream model output incrementally into the session view
status: blocked
priority: 3
created: "2026-07-01"
updated: "2026-07-02"
depends_on: []
spec_refs:
    - 7. Agent engine
    - 18.4 Reasoning (thinking) in the event stream
    - 5. Session & event log
---

## Description
## Description
Model turns render only when complete: on a long coordinator/implementer turn the user watches a spinner for a minute-plus with zero signal — the single largest "feel" gap vs. mainstream harnesses. Design (then implement) incremental turn rendering: a streaming `Turn` in gollama feeding provisional partial-text events (or a client-side streaming channel) that the TUI renders as a live-updating tail row, finalized by the existing `model_turn` event so the durable log shape is unchanged.

This is a design-first task: the event log is append-only and replayed, so partial output must not pollute the log (stream out-of-band, or use a non-persisted event kind). Decide the seam (gollama streaming API → engine → Subscribe stream) with the user before building.

## Acceptance criteria
- [ ] Design note in spec: how partial output flows to clients without corrupting the append-only log/replay
- [ ] TUI shows model text incrementally during a turn (at least for the coordinator/chat actor)
- [ ] Final model_turn event unchanged; replay/transcripts unaffected
- [ ] Graceful fallback for backends without streaming

## Acceptance criteria

## Work log
- 2026-07-02 blocked: parked for the overnight autonomous work-loop run — design-first task (streaming seam + append-only-log invariant + cross-repo gollama work) that the user wants to decide interactively. Unblock after a design discussion records the chosen seam in this task/spec.
