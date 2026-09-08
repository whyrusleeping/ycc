---
id: "0321"
title: Automatic context rollover for long implementer and reviewer loops
status: done
priority: 1
created: "2026-08-12"
updated: "2026-09-08"
depends_on: []
spec_refs:
    - §7.3 Subagents and asynchronous jobs
    - §10 Work orchestration
---

## Intent
Prevent retained implementer/reviewer history from growing until a provider rejects it, while preserving resolved slots, role/focus, reasoning, access policy, round, and single-writer semantics.

## Acceptance criteria
- Logical models expose configurable context-window budgets distinct from per-turn output caps.
- Retained continuations roll over at a configurable safe fraction; context-length errors receive one fresh recovery when safe.
- Fresh handoffs carry current task/tree context, unresolved blocker/major findings, approach and verification evidence without old tool logs.
- Lifecycle events distinguish coordinator-selected fresh context, automatic pressure rollover, and context-error recovery with old/new estimates.
- Regression coverage protects same-slot recreation and prevents duplicate mutation or review submission.

## Outcome
Implemented per-model TOML context budgets, continuation preflight rollover, evidence-carrying fresh handoffs, and one-shot safe error recovery. Implementer recovery is restricted to runs in which no tool executed; reviewer recovery precedes the single review submission. Failed reviews preserve prior valid findings. Existing model settings RPC edits preserve TOML context budgets.

Verified `go test ./...` in both the working workspace and a task-only HEAD assembly, focused race tests, and diff checks. Both independent reviewers accepted after the verification-evidence fix. Unrelated pre-existing changes were excluded from the commit.

Commit subject: Add automatic context rollover for subagent continuations
