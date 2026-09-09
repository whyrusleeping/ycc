---
id: "0356"
title: Account for tool schemas, arguments, and provider state in context estimates
status: done
priority: 2
created: "2026-09-08"
updated: "2026-09-09"
depends_on: []
spec_refs:
    - §7.1 Provider boundary and history
    - §7.3 Subagents and asynchronous jobs
    - §13 Models, credentials, and review tiers
---

## Description
Replace text-only context estimates with selected-backend request accounting, distinct from cumulative usage analytics.

## Acceptance criteria
- Include repeated tool definitions, argument payloads, relevant provider replay state, and supported media with documented approximations.
- Distinguish measured prior input, estimated next-request context, and cumulative billing; use provider measurements where applicable.
- Expose known/configured input capacity separately from output caps, reporting unknown capacity honestly.
- Share visibly approximate accounting between public telemetry and model-pressure hints.
- Cover large Edit/Write calls, schema repetition, provider filtering, media, switching, and legacy/missing usage without claiming exact universal image costs.

## Outcome
Implemented a shared request-shape-aware estimator with provider-measurement calibration, serializer field precedence, same-model Codex replay filtering, and explicitly uncertain media estimates. Configured/known context capacity is propagated through coordinator, subagent, capture, and live model-switch loops independently of output caps. Model/history replacement invalidates calibration. Added focused regression coverage and documented semantics in the spec.

Independent review accepted the implementation and precedence revision; coordinator verified the final small Codex fallback-reasoning correction. The isolated task-only tree passes `go test ./...` and `go test -race ./internal/engine`; `git diff --check` passes. One full-suite run transiently failed the unrelated session integration-state test; the unchanged suite passed on rerun, and 20 baseline repetitions of that test passed. Unrelated pre-existing workspace changes are excluded from the commit.

Commit subject: `Account for complete backend request context in estimates`
