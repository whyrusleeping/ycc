---
id: "0229"
title: Remove the judgement interaction level
status: done
priority: 2
created: "2026-08-04"
updated: "2026-08-04"
depends_on: []
spec_refs:
    - Interaction levels
---

## Description
Remove `judgement` as a selectable/default interaction level across the daemon, CLI, TUI, web/iOS clients, prompts, tests, and current documentation. Keep only `interactive` and `autonomous`; migrate omitted or legacy `judgement` values to the safe attended default (`interactive`). Preserve historical backlog records as historical evidence rather than rewriting completed task logs.

Acceptance criteria:
- New sessions default to `interactive` when no level is provided.
- Public selectors and CLI documentation expose only `interactive | autonomous`.
- Legacy persisted/session/API `judgement` input is handled compatibly as `interactive`, not rejected or treated autonomously.
- Interaction policies, safety/confirmation gates, workstreams, and tests no longer rely on a distinct judgement branch.
- Current spec/design/API/runbook documentation reflects the two-level model.
- Relevant tests and the full Go test suite pass.

## Acceptance criteria

## Work log
