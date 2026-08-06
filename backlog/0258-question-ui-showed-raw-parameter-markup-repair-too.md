---
id: "0258"
title: 'Question UI showed raw parameter markup: repair tool args when a model leaks invoke syntax into a JSON string'
status: in_review
priority: 2
created: "2026-08-06"
updated: "2026-08-06"
depends_on: []
spec_refs: []
---

## Description
Reported from the iOS question sheet: an `ask_user` arrived as a wall of raw XML — the question text ended with `… What next?</question>\n<parameter name="options">["…", "…"]` and no option picker was offered.

Root cause (confirmed in two live logs): the MODEL leaks the XML-ish invoke syntax INTO a JSON string argument — it closes the parameter it is writing with a tag and spells the remaining parameters as markup inside that same string. Observed with claude-opus-5 on `ask_user` (`question` swallowing `options`, valstore s_48f210c2a3df61d5 seq 369) and on `create_task` (`description` swallowing `priority`, oldgrowth s_c3166fafb72f5cfb seq 497). The call is well-formed JSON, so nothing downstream noticed.

## Implementation

- `internal/tools/argrepair.go`: `RepairLeakedArgs(raw, declared)` truncates the host string at the closing tag and moves `<parameter name="X">value` blocks into real arguments (JSON-decoding each value, so `["a","b"]` and `3` land typed). Conservative: needs a closing tag immediately followed by the block AND `X` declared by that tool AND unset in the call.
- `Registry.Repair(call)` applies it schema-aware; the engine loop repairs before emitting `tool_call` (recording `repaired: [names]`), `Dispatch` repairs again for other callers and appends a correction note to the tool result so the model stops repeating it.

## Acceptance criteria

- [x] both real-world payloads are recovered into separate arguments (regression tests use them verbatim)
- [x] honest calls (clean, prose mentioning the syntax, undeclared name, already-set argument, non-JSON args) are untouched
- [x] recovery is visible on the `tool_call` event and to the model
- [x] `go test ./...` passes
- [ ] confirmed live: a follow-up `ask_user` that leaks renders as a clean question + option picker on iOS/TUI

## Work log
