---
id: "0170"
title: 'gollama: native SSE streaming for OpenAI-compatible + Ollama backends (TurnStream parity)'
status: todo
priority: 4
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 7. Agent engine
    - 18.4 Reasoning (thinking) in the event stream
---

## Description
Task 0120 landed `TurnStream` (gollama 4140920) with native SSE streaming for Anthropic only; OpenAI-compatible and Ollama backends currently use the graceful fallback (blocking Turn + one whole-text snapshot delta). The task 0120 description explicitly scoped these as follow-ups.

Implement native streaming for the OpenAI-compatible `/chat/completions` path (`stream: true` chunked deltas, incl. tool-call argument assembly) and Ollama, preserving TurnStream's contract: snapshot-semantics onDelta, final message byte-equivalent to the non-streaming shape, Turn untouched. Work happens in /home/why/code/gollama (clone-into-workspace pattern from task 0120's plan), then bump ycc's go.mod.

## Acceptance criteria
- [ ] OpenAI-compatible TurnStream streams natively (offline recorded-SSE tests: text deltas, tool-call assembly, usage; DeepEqual to non-streaming shape)
- [ ] Ollama path streams (native or via its OpenAI-compatible endpoint — document the choice)
- [ ] Fallback still covers Bedrock; no caller-visible API change
- [ ] ycc go.mod bumped; ycc build/vet/test green

## Work log
