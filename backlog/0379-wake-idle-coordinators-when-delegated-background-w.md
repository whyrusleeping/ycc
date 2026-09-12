---
id: "0379"
title: Wake idle coordinators when delegated background work finishes
status: done
priority: 3
created: "2026-09-11"
updated: "2026-09-12"
depends_on: []
spec_refs:
    - 7.3 Subagents and asynchronous jobs
---

## Description
Add runtime completion-driven continuation when a coordinator has sent a final response while its session-owned jobs remain live. Preserve checkpoint delivery for running parents, exactly-once notification/wait suppression, serialized parent execution and user input, and explicit pause/stop/refusal/error boundaries. Combine available completions and use synthetic completion context, not user intent. Guide agents to report progress then wait when no independent work remains. Update durable docs and add targeted concurrency/lifecycle regression coverage.

## Acceptance criteria

## Work log

- Implemented event-driven idle continuation with coalesced, exactly-once synthetic completion delivery; tracked jobs become automatically deliverable only after execution and mutation ownership are released. Explicit pause/resume, blocked/error/refusal/reopen boundaries, and ordered user-input handoff are preserved.
- Updated agent guidance to report progress then wait. Work-loop/reaper retention and workstream readiness honor outstanding continuation; idle progress reports carry `awaiting_jobs`.
- Verification: `go test ./...`, `go build ./...`, `git diff --check`, targeted session race regressions (five runs), and jobs/tools/orchestrator race suites passed. Broad session race suite encountered the pre-existing `TestRetryIntegrationNeedsAttentionTransitionsReadyAndEmits` flake (also reproduced at HEAD during implementation). Changes left uncommitted alongside existing work.
