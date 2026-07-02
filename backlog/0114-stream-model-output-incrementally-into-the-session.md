---
id: "0114"
title: Stream model output incrementally into the session view
status: todo
priority: 3
created: "2026-07-01"
updated: "2026-07-05"
depends_on:
    - "0120"
spec_refs:
    - 7. Agent engine
    - 18.4 Reasoning (thinking) in the event stream
    - 5. Session & event log
---

## Description
Model turns render only when complete: on a long coordinator/implementer turn the user
watches a spinner for a minute-plus with zero signal — the single largest "feel" gap vs.
mainstream harnesses.

**Design decided with the user (2026-07-05 pm session): Option A — transient,
non-persisted events through the existing Subscribe pipe.**

- gollama grows a streaming `TurnStream` API first — that is task **0120** (this task
  depends on it and covers only the ycc side).
- The engine loop consumes streamed deltas and emits `turn_delta` events that
  `event.Log` **broadcasts to live subscribers but never persists**: no seq assigned,
  never written to events.jsonl, never appended to the in-memory replay slice. A clear
  `transient` marker distinguishes them; subscribers must tolerate seq-less events.
- The existing `Server.Subscribe` RPC carries the deltas unchanged, so every client
  (TUI, future remote clients) gets streaming for free.
- The TUI renders a live-updating tail row per streaming actor; the final (persisted,
  unchanged) `model_turn` event replaces it. Replay/transcripts are naturally unaffected
  because deltas never touch disk.
- Backends without streaming fall back inside gollama (one whole-text delta), so the
  ycc side needs no per-backend branching.

## Acceptance criteria
- [ ] Design note in spec: how partial output flows to clients without corrupting the append-only log/replay (transient `turn_delta` events, never persisted)
- [ ] `event.Log` broadcast-without-persist path; replay slice and events.jsonl provably never contain deltas
- [ ] TUI shows model text incrementally during a turn (at least for the coordinator/chat actor)
- [ ] Final model_turn event unchanged; replay/transcripts unaffected
- [ ] Graceful fallback for backends without streaming (via gollama 0120 fallback)

## Work log
- 2026-07-05 design pass with user (pm session): seam decided — Option A (transient
  non-persisted events through the existing Subscribe stream), split into two tasks:
  0120 (gollama TurnStream, cross-repo prerequisite) and this one (ycc plumbing + TUI
  tail row). Unblocked; now depends on 0120.
- 2026-07-02 blocked: parked for the overnight autonomous work-loop run — design-first task (streaming seam + append-only-log invariant + cross-repo gollama work) that the user wants to decide interactively. Unblock after a design discussion records the chosen seam in this task/spec.
