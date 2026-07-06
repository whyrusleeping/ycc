---
id: "0171"
title: Stream thinking deltas through TurnStream and the turn_delta path
status: proposed
priority: 5
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 18.4 Reasoning (thinking) in the event stream
---

## Description
Follow-on idea from task 0120: gollama's Anthropic SSE assembler already accumulates thinking_delta chunks but only assistant TEXT is forwarded via onDelta; live subscribers see nothing while the model reasons. Extend the streaming surface so thinking text can also be tailed live.

Sketch: add a richer callback variant in gollama (e.g. TurnStreamEx with a delta struct carrying text + thinking snapshots) without breaking the existing TurnStream signature that ycc's engine.StreamTurner seam depends on; extend ycc's transient turn_delta payload (e.g. {"thinking": ...}) per spec §18.4 and render a live reasoning tail in the TUI.

## Acceptance criteria
- [ ] gollama exposes thinking snapshots during Anthropic streamed turns (additive API; TurnStream unchanged)
- [ ] ycc broadcasts them transiently (never persisted) and the TUI shows a live reasoning tail
- [ ] spec §5.2/§18.4 updated for the extended payload

## Work log
