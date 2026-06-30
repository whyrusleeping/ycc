---
id: "0092"
title: Bump capture agent MaxTurns to 32 and tell agent its turn budget
status: todo
priority: 3
created: "2026-06-30"
updated: "2026-06-30"
depends_on: []
spec_refs: []
---

## Description
## Description
The capture agent's turn limit is too small. In `internal/orchestrator/capture.go`, `RunCapture` builds the `engine.Loop` with `MaxTurns: 16`, which can be too tight for grounding the task (a few reads + a backlog check) before calling `create_task`.

Raise the limit to 32, and make the agent aware of its turn budget so it can pace its investigation and not get cut off mid-work. The turn count is currently hardcoded both in the loop config (`MaxTurns`) and would need to be reflected in the system prompt (`captureSystem`), so consider a single source of truth (a const) used for both the loop and the prompt text.

## Acceptance criteria
- `MaxTurns` for the capture loop is 32 (not 16).
- The capture system prompt informs the agent how many turns it has, kept in sync with the actual `MaxTurns` value (e.g. via a shared constant) so they cannot drift.
- Existing capture tests still pass; add/adjust a test if one asserts on the turn limit or prompt text.

## Work log


## Acceptance criteria

## Work log
