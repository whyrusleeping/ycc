---
id: "0595"
title: Native Gemini backend (gollama + ycc) for gemini-4-neon
status: done
priority: 2
created: "2026-11-23"
updated: "2026-11-24"
depends_on: []
spec_refs:
    - spec.md#7.1 Provider boundary and history
    - spec.md#7.4 Reasoning settings
---

## Description

Status: in progress. The gollama change has to be pushed before ycc's go.mod can pin it.

## Acceptance criteria

- A multi-turn, tool-using ycc session on `gemini-4-neon` works, including after daemon reopen and after a coordinator switch from Claude to Gemini partway through a turn.
- The configured thinking level reaches the request as thinkingLevel, and reasoning summaries appear as reasoning.
- Usage and cost are recorded with cached tokens counted.
- Gemini thought signatures are never sent to other backends.

## Outcome

backend = "gemini" calls the native Gemini generateContent API through gollama c6eaaa5 (pushed to gollama main), keyed by x-goog-api-key. It maps thinkingLevel (low/medium/high, plus xhigh for gemini-4-neon; saved effort is clamped), surfaces thought summaries, round-trips per-call thought signatures (recorded on tool_call events, restored on replay, dummy signature for unsigned current-turn calls), preserves media in tool results, classifies Gemini error bodies, reports partial-output progress via OnGenerated, and prices Neon at $3/$15/$0.15 cached. Model discovery, credential scrubbing, web and iOS backend pickers, spec and README were updated. Verified with mock-server tests and a full go test/vet on a clean candidate tree; the iOS build and a picker screenshot were checked on the Mac. No live API test (no key).

Commit: Native gemini backend for gemini-4-neon (0595)
