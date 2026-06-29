---
id: "0043"
title: Unify tool-call params and response into one collapsed chat-log row
status: done
priority: 3
created: "2026-06-27"
updated: "2026-06-29"
depends_on:
    - "0006"
spec_refs:
    - Client UI (TUI)
---

## Description
## Description
In the TUI chat log, a tool call currently renders its params (args) and its response as
separate rows. When collapsed (unexpanded), these should be unified into a *single* row so the
call and its result read as one logical entry. Expanding the row should still reveal the full
params and full response.

This is a presentation change to the chat-log event rendering in the TUI; it should preserve
existing syntax highlighting / JSON-args / diff / cat -n formatting once expanded.

## Acceptance criteria
- [ ] A tool call and its corresponding response render as one row when collapsed
- [ ] The collapsed row shows enough to identify the tool + a concise summary (params and/or
      result status)
- [ ] Expanding the row reveals the full params and the full response (no information lost)
- [ ] Existing per-content formatting (JSON args, diffs, cat -n, highlighting) is preserved
      when expanded
- [ ] No regression to scrolling/selection or to non-tool-call event rows

## Acceptance criteria

## Work log
- 2026-06-29 plan: Unify an adjacent tool_call + tool_result into a single chat-log row in internal/tui/tui.go.  Key insight: in the engine loop a tool_call event is always immediately followed by its tool_result event 
…[truncated]
- 2026-06-29 implementer report: Implemented task 0043: unified an adjacent tool_call + tool_result into a single collapsed chat-log row in internal/tui/tui.go.  Changes in internal/tui/tui.go: - Added `mergedResultIdx(i)` (bounds-sa
…[truncated]
- 2026-06-29 review tier: single-opus — reviewers: Claude
- 2026-06-29 review (Claude): accept — The change cleanly unifies an adjacent tool_call + tool_result into a single collapsed chat-log row in internal/tui/tui.go. Pairing is by adjacency + actor + id, which correctly excludes spawn-style t
…[truncated]
- 2026-06-29 decision: accept — commit f64fc96: Unify adjacent tool_call + tool_result into one collapsed chat-log row  Pair a tool_call with its immediately-following tool_result (same actor, matching id) and render them as a single chat-log row: 
…[truncated]
- 2026-06-29 usage: 44,256 tok (in 116, out 44,140, cache_r 2,151,271, cache_w 94,881) · cost n/a (unpriced)
