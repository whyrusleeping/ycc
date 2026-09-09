---
id: "0359"
title: Give generic agents fresh-context continuation and evidence-aware handoffs
status: done
priority: 2
created: "2026-09-08"
updated: "2026-09-09"
depends_on: []
spec_refs:
    - §7.3 Subagents and asynchronous jobs
---

## Description
Generic send_to_agent always retains history and exposes less context/recovery information than implementer/reviewer continuation. Bring the useful controls to generic agents without multiplying role-specific APIs. Evidence: internal/orchestrator/generic_agent.go:130–185; existing context_mode handling in orchestrator.go.

## Acceptance criteria
- Generic follow-up supports explicit retain/fresh context choice with documented handle/model/access semantics.
- Fresh handoffs carry a bounded self-contained request, relevant evidence/artifact references, unresolved questions, and verification requirements; do not silently replay obsolete full logs.
- Return context-pressure and round information consistent with other subagent types.
- After context-length failure, provide a usable fresh recovery path rather than reposting to an unusable retained loop.
- Preserve single-writer ownership, live-job restrictions, cancellation, and durable lifecycle events.
- Tests cover retained continuity, fresh replacement, failed prior turn, permissions/model identity, and follow-up during a still-running job.

## Outcome
Implemented retain/fresh generic follow-ups with bounded evidence-aware handoffs, stable model/access/handle identity, round/context metadata, and actionable fresh recovery after context-length failure. Existing ownership, live-job and cancellation guards remain intact. Updated tool guidance and spec §7.3; focused regression tests cover continuity, replacement, recovery and permissions. Independent review accepted; full Go suite and orchestrator race tests pass in both the working tree and an isolated task-only tree. Unrelated pre-existing changes excluded from the commit.

Commit subject: Add fresh-context continuation for generic agents
