---
id: "0063"
title: Event-stream visual polish (type glyphs, verdict colors, subagent tree, selection bar)
status: todo
priority: 3
created: "2026-06-29"
updated: "2026-06-29"
depends_on:
    - "0060"
spec_refs:
    - Client UI (TUI)
---

## Description

The session event stream is readable but visually flat and slow to scan. Every row leads
with a fixed-width actor column and a gray type word; subagent work is indented by two
blank spaces; review verdicts render uncolored; and selection recolors the whole row's
text. This task is a focused polish pass on the per-event rendering to make the transcript
faster to scan and nicer to look at.

### Design (all consuming the 0060 palette)
- **Per-type glyphs**: prefix each event with a small icon for fast scanning — e.g. tool
  call (wrench), thinking (lightbulb), tool result ok/err (✓/✗), review (gavel), commit
  (dot), model turn (speech), user input (›). Keep them ASCII/unicode-safe and aligned.
- **Colorize review verdicts**: approve/accept = success, revise = warn, reject = danger
  (currently `review_submitted` renders uncolored in `detailLine`/`renderBody`).
- **Subagent tree connectors**: replace the bare two-space subagent indent (`isSub`) with
  tree guides (`└─` / `│`) so implementer/reviewer events read as nested under the
  coordinator.
- **Selection treatment**: keep the `▌` selection bar but consider a subtle
  background-highlight on the selected row instead of recoloring the entire row text, so
  long rows stay legible when selected.
- Preserve the collapsed/expanded affordances (`▸`/`▼`), the merged tool_call+tool_result
  row, and all click/keyboard selection mapping (`eventStart`, `eventAt`).

## Acceptance criteria
- [ ] Each event type renders a consistent leading glyph; columns stay aligned across rows.
- [ ] Review verdicts are color-coded (approve/revise/reject) in both the collapsed detail
      line and the expanded body.
- [ ] Subagent (implementer/reviewer) events render with tree connectors indicating nesting
      under the coordinator.
- [ ] Selection highlighting is legible on long rows; click/keyboard expand-collapse and
      the merged call+result row continue to work, with `eventStart`/`eventAt` line mapping
      intact.
- [ ] TUI unit tests covering rendering/selection/expansion still pass; updated for the new
      glyph/connector strings.

## Notes
- Depends on 0060 (named color roles for glyph/verdict/connector colors).
- Be conservative with glyphs — they must not break the line-offset accounting used for
  click-to-expand (`rebuild()` counts newlines per block).

## Work log
