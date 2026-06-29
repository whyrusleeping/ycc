---
id: "0062"
title: Live session status bar (mode/level/thinking/elapsed/token-cost) + activity spinner
status: done
priority: 3
created: "2026-06-29"
updated: "2026-06-29"
depends_on:
    - "0060"
spec_refs:
    - Client UI (TUI)
    - Usage & cost accounting
---

## Description

The session status bar is a single flat gray line: `id · mode:x · status`. It hides
information the user cares about and feels static even while the agent is busy — `status`
just reads `running` with no animation. Meanwhile `model_turn` events already carry a
`usage` block (parsed by `internal/usage`), so running token/cost is available but never
surfaced in the session view.

This task turns the header into a richer, live status bar and adds an activity indicator.

### Design
- Replace the flat header with **segmented pills** (using the 0060 palette): a colored
  **mode** pill, an **interaction-level** badge, the **thinking** level, **elapsed time**
  for the session/current turn, and a live **token / cost** readout.
- Compute running usage by summing the `usage` field across `model_turn` events in the
  event stream (reuse `internal/usage` parsing where practical). Show cost when pricing is
  available, tokens otherwise; render "unpriced" gracefully.
- Add a small **spinner** (`bubbles/spinner`) that animates while `status == running`
  (and reuse it for the quick-capture overlay's "capturing…" state). It must tick via the
  Bubble Tea command loop and stop when idle/paused/error.
- Keep the single-physical-row invariant: the bar must clamp/truncate to width so it never
  wraps and pushes the frame down (the existing header bug class). Degrade gracefully on
  narrow terminals by dropping lower-priority segments first.

## Acceptance criteria
- [ ] Session header shows mode, interaction level, thinking level, elapsed time, and a
      running token/cost readout as distinct styled segments.
- [ ] Running token/cost is derived from `model_turn` usage and updates as turns stream in;
      unpriced sessions show tokens without crashing or showing a bogus cost.
- [ ] A spinner animates while the session is `running` and stops on idle/paused/error;
      the quick-capture overlay shows the spinner while a capture RPC is in flight.
- [ ] The header remains exactly one physical row at all terminal widths (no wrap / frame
      overflow), degrading by dropping segments when space is tight.
- [ ] TUI unit tests cover the usage summation and the status-bar segment rendering.

## Notes
- Depends on 0060 (palette/roles for the pills and spinner color).
- Spec §20 (usage & cost) — this is the live, in-session surfacing of that data.
- Watch the existing latched-`error` status handling (task 0051): the spinner must not
  resurrect on a stale error state.

## Work log
- 2026-06-29 plan: Turn the flat session header into a richer, live, segmented status bar with running token/cost and an activity spinner.  1) Surface pricing to the TUI (so cost can be computed live):    - proto/ycc/v1
…[truncated]
- 2026-06-29 implementer report: Implemented task 0062: live, segmented session status bar with running token/cost readout plus an activity spinner.  ## Changes  **Pricing surfaced through ListModels** - `proto/ycc/v1/ycc.proto`: ext
…[truncated]
- 2026-06-29 review tier: single-opus — reviewers: Claude
- 2026-06-29 review (Claude): accept — The change fully and correctly implements task 0062. Pricing is surfaced through ListModels with optional price fields and a `priced` flag (server never invents a cost for unpriced models). The TUI he
…[truncated]
- 2026-06-29 decision: accept — commit: tui: live segmented session status bar (mode/level/thinking/elapsed/token-cost) + spinner [0062]  Surface per-model pricing through ListModels (optional price_* fields + priced flag) and rebuild the s
…[truncated]
