---
id: "0114"
title: Stream model output incrementally into the session view
status: blocked
priority: 3
created: "2026-07-01"
updated: "2026-07-03"
depends_on:
    - "0120"
    - "0128"
    - "0129"
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
- 2026-07-07 blocked (autonomous coordinator): all gollama-independent scope is done
  (0128 transient broadcast path + 0129 engine StreamTurner seam/TUI tail row are done;
  the loop already type-asserts StreamTurner and emits throttled transient turn_delta).
  The only remaining work — making the real gollama client implement TurnStream (task
  0120) and live end-to-end verification — is gated on the gollama working repo at
  /home/why/code/gollama, which is still absent in this environment. Verified upstream
  gollama HEAD is still the pinned 567eebc with no TurnStream, so nothing can be adopted
  from the module cache either. Note: list_backlog shows this task [READY] because dep
  0120 is "blocked" rather than an undone todo — do not start it until 0120 is done.
  Unblock together with 0120 once the gollama working repo is available.
- 2026-07-06 (autonomous coordinator): 0120 remains blocked (gollama working repo still
  unavailable in this environment), so split the remaining gollama-independent scope into
  task 0129: engine StreamTurner seam (snapshot-semantics onDelta, throttled transient
  turn_delta broadcasts, retry-capability forwarding, graceful non-streaming fallback) +
  the TUI live tail row, all testable with fakes. Added 0129 as a dependency. What
  remains here after 0129: a small adapter wiring gollama TurnStream to the engine
  StreamTurner seam once 0120 lands, plus live end-to-end verification of incremental
  output in the TUI.
- 2026-07-05 (autonomous coordinator): split out the gollama-independent transport
  groundwork into task 0128 (event.Log transient broadcast path, transient/turn_delta
  marker, Server.Subscribe pass-through, TUI tolerance, spec design note) and added it
  as a dependency; this task now covers consuming gollama TurnStream in the engine loop
  (emitting turn_delta) + the TUI live tail row, once 0120 lands.
- 2026-07-05 design pass with user (pm session): seam decided — Option A (transient
  non-persisted events through the existing Subscribe stream), split into two tasks:
  0120 (gollama TurnStream, cross-repo prerequisite) and this one (ycc plumbing + TUI
  tail row). Unblocked; now depends on 0120.
- 2026-07-02 blocked: parked for the overnight autonomous work-loop run — design-first task (streaming seam + append-only-log invariant + cross-repo gollama work) that the user wants to decide interactively. Unblock after a design discussion records the chosen seam in this task/spec.
- 2026-07-03 decision: accept — commit: backlog: park 0114 as blocked on gollama TurnStream (0120); record 0129 usage note
