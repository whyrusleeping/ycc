---
id: "0202"
title: 'Codex: emit full snapshot deltas from TurnStream'
status: todo
priority: 2
created: "2026-07-15"
updated: "2026-07-15"
depends_on: []
spec_refs:
    - Session & event log#Transient (broadcast-only) events
    - Agent engine
---

## Description
The engine `StreamTurner` contract requires each callback to contain the full accumulated assistant text snapshot. `internal/codex` currently forwards raw incremental fragments, so the TUI's replacement-style live tail can display only the latest fragment and throttling can leave misleading partial output.

## Acceptance criteria
- [ ] After every output-text fragment, Codex invokes `onDelta` with the full accumulated visible text so far.
- [ ] For fragments `hel` and `lo`, the callback sequence is exactly `hel`, `hello` rather than `hel`, `lo`.
- [ ] The final returned assistant content is unchanged and is equivalent between `Turn` and `TurnStream`.
- [ ] Tool-call-only turns may emit no text snapshots, as allowed by the engine contract.
- [ ] Tests assert callback values individually rather than joining fragments in a way that masks contract violations.
- [ ] Live retry/clear-tail behavior remains compatible with `engine.Loop.turnOnce`.
- [ ] `go test ./...` passes.

## Work log
