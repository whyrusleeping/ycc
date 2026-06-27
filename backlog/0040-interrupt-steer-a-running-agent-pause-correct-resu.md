---
id: "0040"
title: Interrupt & steer a running agent (pause / correct / resume)
status: done
priority: 2
created: "2026-06-27"
updated: "2026-06-27"
depends_on: []
spec_refs:
    - Client UI (TUI)
    - RPC protocol (Connect)
    - Agent engine
---

## Description
## Description

Let the human **interrupt a running agent** and either let it carry on as if nothing
happened, or correct it before it acts further. Today `SendInput` only reaches the agent
*between* `Run` calls (when the session is idle); mid-run, user text just sits buffered in
`Session.inputCh` (see `internal/session/session.go` SendInput + the run loop ~L383-421)
and is not consumed until the current run finishes. So a user watching the agent head down
the wrong path cannot redirect it mid-flight — only wait, then correct after the wrong work
is done.

This is a **graceful pause to steer**, distinct from a hard Stop (task 0009): after the
pause the agent continues on the *same* engine `Loop` and conversation. See spec §18.7 for
the full model.

### Design (per spec §18.7, §12, §7.2)

- **Engine (`internal/engine/loop.go`).** Add a `Steer` hook the `Loop.Run` consults at safe
  checkpoints — at the top of each turn iteration and after each tool result:
  `Checkpoint(ctx) ([]string, error)`. If a pause is pending it blocks until resume (or ctx
  cancellation, returned as a normal stop), then returns any correction messages, which the
  loop appends via `Post` before the next turn. Nil/absent `Steer` ⇒ cheap no-op; the hot
  loop is unaffected. The loop does **not** abort a tool mid-execution.
- **Session (`internal/session`).** Implement `Steer`: a pause flag, a resume signal, and a
  buffer of steered-in corrections. `Interrupt()` sets the pause request (status → `paused`,
  emit `interrupted`). `SendInput` while paused appends the text as a correction and resumes
  (it currently buffers to `inputCh` — paused path must route into the steer buffer instead
  and signal resume). `Resume()` continues with no correction. On resume, emit `resumed`,
  status → `running`. A blocked-on-question session is already suspended (interaction layer) —
  steer-interrupt targets the *running* case.
- **Proto / server.** Add `Interrupt(session_id)` and `Resume(session_id)` RPCs (spec §12)
  with `InterruptRequest`/`ResumeRequest` (+ empty responses) and wire `Server` handlers to
  `Session.Interrupt()` / `Session.Resume()`. Keep the hard-stop RPC name as `Stop` (task
  0009) so the two are not conflated.
- **Events.** Add `interrupted` and `resumed` event types (spec §5.2). State lives in the log
  so any subscribed client (incl. future phone) sees and can drive the pause.
- **TUI (`internal/tui/tui.go`).** A session-view key (e.g. `ctrl+i`) issues `Interrupt`;
  render the paused state distinctly ("⏸ paused — type a correction and Enter to steer, or
  Resume to continue"); Enter on a non-empty buffer steers (`SendInput`), an explicit Resume
  action (empty buffer / a key) issues `Resume`.

### Optional enhancement (may defer)
For immediacy during a long in-flight **model turn**, run the turn under a child context that
`Interrupt` cancels, discarding that turn's output (no history append) and dropping straight
to the checkpoint. Baseline (pause only at the next checkpoint) is acceptable for a first cut.

## Acceptance criteria
- [ ] A running session can be paused via `Interrupt(session_id)`; it stops at the next safe
      checkpoint (between turns / after a tool result) without aborting a tool mid-run, emits
      an `interrupted` event, and reports status `paused`.
- [ ] While paused, `Resume(session_id)` continues the *same* loop/conversation unchanged and
      emits `resumed` (status → `running`).
- [ ] While paused, `SendInput(text)` injects the text into the loop's conversation as a user
      message and resumes, so the agent's next turn incorporates the correction; multiple
      messages before resume land in order.
- [ ] When not paused, the steer checkpoint is a no-op (no behavior/perf change to normal runs;
      existing tests still pass).
- [ ] A session cancelled (Stop / ctx cancel) while paused unblocks cleanly.
- [ ] TUI affordance to interrupt, see the paused state, and either steer or resume.
- [ ] Engine + session unit tests cover: pause→resume unchanged, pause→correct→agent sees the
      message, and pause→cancel unblock.

## Notes
- Relationship to **task 0009**: that task is the hard Stop/GC (terminate). This task owns the
  graceful `Interrupt`/`Resume` (pause-to-steer) RPC names; 0009's terminate should be `Stop`.
  They can land in either order; coordinate the RPC naming to avoid a clash.

## Acceptance criteria

## Work log
- 2026-06-27 plan: Implement graceful pause-to-steer (spec §18.7), distinct from task 0009's hard Stop.  1. Engine (internal/engine/loop.go): add a `Steer` interface field on `Loop` with    `Checkpoint(ctx) ([]string, 
…[truncated]
- 2026-06-27 plan: Implement graceful pause-to-steer (spec §18.7), distinct from task 0009's hard Stop.  1. Engine (internal/engine/loop.go): add a `Steer` interface    `Checkpoint(ctx context.Context) ([]string, error
…[truncated]
- 2026-06-27 implementer report: Implemented graceful pause-to-steer (Interrupt/Resume) for a running agent per spec §18.7, distinct from task 0009's hard Stop.  ## Changes - **internal/engine/loop.go**: Added a `Steer` interface (`
…[truncated]
- 2026-06-27 review (claude): revise — The change implements graceful pause-to-steer largely per spec: engine gains a clean Steer/Checkpoint hook (nil = no-op fast path, checkpoints between turns and after tool results, no mid-tool abort),
…[truncated]
- 2026-06-27 revision: Addressed both review issues.  ## 1. Race in steered corrections (blocker) — fixed Adopted the deterministic buffer-until-Resume model: - **internal/session/session.go**: The paused/pausing branch o
…[truncated]
- 2026-06-27 review (claude): accept — The revision resolves both prior findings. SendInput while paused now only buffers corrections (drained in order by an explicit Resume) rather than auto-resuming, eliminating the race; TestSteerPauseC
…[truncated]
- 2026-06-27 decision: accept — commit 5792ca8: Interrupt & steer a running agent (pause / correct / resume) — §18.7  Add a graceful pause-to-steer for a running session, distinct from a hard Stop: - engine: Loop gains a Steer hook (Checkpoint) 
…[truncated]
- 2026-06-27 usage: 49,961 tok (in 170, out 49,791, cache_r 5,565,434, cache_w 175,274) · cost n/a (unpriced)
