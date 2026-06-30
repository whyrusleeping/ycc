---
id: "0092"
title: Bump capture agent MaxTurns to 32 and tell agent its turn budget
status: done
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

## Plan

In internal/orchestrator/capture.go:
1. Add a package const `captureMaxTurns = 32` near captureSystem.
2. Use it in the Loop config: `MaxTurns: captureMaxTurns` (was 16).
3. Make the system prompt turn-budget aware. Since captureSystem is a const string, build the prompt with the turn count interpolated from captureMaxTurns (e.g. turn captureSystem into a fmt.Sprintf using %d, or append a sentence) so the number can't drift. Add a sentence like: "You have a budget of N turns (model+tool steps); pace your investigation so you don't get cut off before calling create_task."
4. Verify/adjust tests: run capture tests; if any assert MaxTurns==16 or prompt text, update them. Add a small test asserting the prompt mentions the same number as captureMaxTurns if reasonable.

Build + go test ./internal/orchestrator/...

## Work log


## Acceptance criteria

## Work log
- 2026-06-30 plan: In internal/orchestrator/capture.go: 1. Add a package const `captureMaxTurns = 32` near captureSystem. 2. Use it in the Loop config: `MaxTurns: captureMaxTurns` (was 16). 3. Make the system prompt tur
…[truncated]
- 2026-06-30 context hints: internal/orchestrator/capture.go: captureSystem const (line ~48), Loop construction with MaxTurns: 16 (line ~134), System: captureSystem + workspaceNote (line ~130)
- 2026-06-30 implementer report: Bumped the capture agent's turn budget from 16 to 32 and made the prompt budget-aware with a single source of truth.  Changes in internal/orchestrator/capture.go: - Added package const `captureMaxTurn
…[truncated]
- 2026-06-30 review tier: simple (coordinator self-review)
- 2026-06-30 decision: accept — commit: capture: bump MaxTurns to 32 and make prompt turn-budget aware (0092)
