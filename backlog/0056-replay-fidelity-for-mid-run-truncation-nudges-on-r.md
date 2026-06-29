---
id: "0056"
title: Replay fidelity for mid-Run truncation nudges on reopen
status: done
priority: 3
created: "2026-06-29"
updated: "2026-06-29"
depends_on:
    - "0034"
spec_refs:
    - 18.6 Session history browser & reopen
---

## Description
## Description
When a session is reopened (resume = replay, task 0034 / spec §4.5/§18.6), the reconstructed
coordinator history can contain two consecutive assistant messages if the original run hit a
mid-Run output-token truncation: the live loop appends a sanitized assistant stub plus an
internal user "nudge" message ("Your previous response was cut off…"), but that nudge is
emitted via `Loop.Post` and is NOT recorded in the event log. So `engine.ReplayHistory`
replays the truncated stub turn followed directly by the retry turn with no intervening user
message, violating strict user/assistant alternation expected by some backends.

Task 0034 already mitigates the signature-rejection risk by dropping thinking blocks from a
truncated turn during replay; this task closes the ordering gap.

## Options
- Log the truncation nudge as a recorded event (e.g. a `user_input`-like or a dedicated
  internal event) so replay reproduces it; ensure the TUI doesn't render it confusingly, or
- Have `ReplayHistory` synthesize a nudge user message when it detects two adjacent
  coordinator assistant turns (the first being a truncated turn).

Also consider the related lossy edge: tool-result images/PDFs are not restored on replay
(only counts are logged) — decide whether to capture enough to round-trip multimodal tool
results, or document the limitation explicitly.

## Acceptance criteria
- [ ] A session that experienced a mid-Run truncation retry reconstructs to a valid,
      alternating conversation on reopen (no two consecutive assistant turns that would be
      rejected by the backend).
- [ ] Test covering reconstruction across a truncation-retry boundary.
- [ ] Multimodal tool-result round-trip on replay is either implemented or explicitly
      documented as unsupported in code + spec.

## Acceptance criteria

## Work log
- 2026-06-29 plan: Fix replay so a mid-Run truncation-retry boundary reconstructs to a valid, strictly-alternating conversation (option 2 from the task: synthesize the nudge during ReplayHistory).  1. internal/engine/lo
…[truncated]
- 2026-06-29 implementer report: Implemented task 0056 (replay fidelity for mid-Run truncation nudges).  Changes: 1. internal/engine/loop.go — extracted the two literal truncation-boundary strings into package-level constants `trun
…[truncated]
- 2026-06-29 review tier: single-opus — reviewers: Claude
- 2026-06-29 review (Claude): accept — The change cleanly closes the ordering gap: it extracts the truncation stub/nudge strings into shared constants, and ReplayHistory synthesizes the unrecorded user nudge when reconstructing a truncatio
…[truncated]
- 2026-06-29 decision: accept — commit 81b2012: engine: synthesize truncation nudge on replay for valid alternation [0056]
- 2026-06-29 usage: 12,513 tok (in 54, out 12,459, cache_r 486,848, cache_w 41,027) · cost n/a (unpriced)
