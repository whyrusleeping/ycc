---
id: "0057"
title: Surface model/tool timing (duration_ms) in TUI chat-log rows
status: done
priority: 4
created: "2026-06-29"
updated: "2026-06-29"
depends_on:
    - "0055"
spec_refs: []
---

## Description
Task 0055 added `duration_ms` to model_turn, tool_result, and session_error events
(persisted in the JSONL log and rendered by event.Render). The primary user-facing
surface — the TUI chat-log rows (`detailLine` in internal/tui/tui.go) — should also show
the elapsed duration so per-turn / per-tool-call timing is visible while scanning a session.

Note: a working implementation already exists as uncommitted WIP in the workspace (helpers
`appendDur`, `durationMSField`, `fmtDurMS` plus `detailLine` edits for model_turn/tool_result),
currently entangled with the in-flight task 0035 (TUI session browser) changes in tui.go. When
0035 lands, confirm this TUI timing display is included; if 0035's WIP is reworked or discarded,
re-apply the timing display cleanly.

## Acceptance criteria
- [ ] Collapsed model_turn and tool_result chat-log rows show a compact duration suffix
      (e.g. "340ms" / "1.2s"), dim-styled, only when duration_ms > 0
- [ ] No regression to existing row rendering, scrolling, or selection
- [ ] Covered by a TUI test

## Acceptance criteria

## Work log
- 2026-06-29: the timing-display implementation (`appendDur`, `durationMSField`,
  `fmtDurMS` + `detailLine`/`renderCombined` edits) LANDED in commit 14f6b76 (bundled
  with task 0011 per maintainer decision). Functionally complete and green; the only
  remaining acceptance criterion is a dedicated TUI test. Do NOT re-implement — just add
  the test, then mark done.
- 2026-06-29 review tier: simple (coordinator self-review)
- 2026-06-29 decision: accept — commit 5e16b22: TUI: add tests for chat-log duration display (fmtDurMS, durationMSField, detailLine) [0057]
- 2026-06-29 usage: 2,471 tok (in 14, out 2,457, cache_r 98,433, cache_w 3,044) · cost n/a (unpriced)
