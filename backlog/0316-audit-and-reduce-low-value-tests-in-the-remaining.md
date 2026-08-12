---
id: "0316"
title: Audit and reduce low-value tests in the remaining Go packages
status: done
priority: 4
created: "2026-08-10"
updated: "2026-08-11"
depends_on:
    - "0312"
spec_refs:
    - CONTRIBUTING.md#Tests
---

## Description
Apply the evidence-based test standard to Go packages outside the orchestrator/config/TUI pilot. Remove duplicated branch and formatting coverage while preserving protection for high-risk boundaries such as auth, persistence, event durability, git/worktree safety, concurrency, replay, accounting, parsing, and previously observed bugs.

## Acceptance criteria
- Candidate tests are evaluated by the plausible regression they catch and whether another lower-level or boundary test already catches it.
- Repetitive implementation-detail tests are removed or consolidated without broad rewrites done only to reduce line count.
- Security, credentials, destructive git operations, path confinement, event durability, concurrency, replay, budgets, and accounting retain strong focused tests.
- Slow, noisy, or flaky tests are repaired, isolated as explicit integration/live tests, or removed when they provide no unique protection.
- No coverage target or deletion quota is introduced.
- The full Go test suite passes, with live tests remaining opt-in.

## Plan

1. Inventory all Go tests outside the already-audited `internal/orchestrator`, `internal/config`, and `internal/tui` packages, grouping candidates by the observable contract or failure boundary they protect and checking for duplicate coverage at lower-level boundaries.
2. Remove or consolidate only tests that pin implementation details, exact formatting/prose, trivial assignments/accessors, exhaustive low-risk branches, or behavior already protected elsewhere. Keep production code unchanged and avoid broad rewrites or any deletion/coverage quota.
3. Preserve focused protection for auth and credentials, persistence and event durability, path confinement, destructive git/worktree behavior, concurrency/cancellation, replay, parsing, budgets/accounting, protocol boundaries, and known regressions. Keep explicit integration/live tests opt-in; repair or isolate genuinely flaky/noisy tests only if encountered and justified.
4. Run gofmt on touched test files, targeted tests for every touched package, then `go test ./...`; distinguish any known/pre-existing flakes from regressions and rerun narrowly where needed.
5. Review the final diff package by package against `CONTRIBUTING.md#Tests`, confirming every deletion is redundant/low-value and no high-risk boundary or production behavior was weakened.

### Starting points
- `CONTRIBUTING.md#Tests` is the governing risk-based standard.
- Task 0312 already audited `internal/orchestrator`, `internal/config`, and `internal/tui`; exclude those areas.
- The working tree contains backlog metadata from prior completed tasks; do not edit or revert unrelated backlog files.

## Work log
- 2026-08-11 plan: 1. Inventory all Go tests outside the already-audited `internal/orchestrator`, `internal/config`, and `internal/tui` packages, grouping candidates by the observable contract or failure boundary they p
…[truncated]
- 2026-08-11 context hints: 3 recorded with plan
- 2026-08-11 context hints: `CONTRIBUTING.md#Tests` is the governing risk-based standard.; Task 0312 already audited `internal/orchestrator`, `internal/config`, and `internal/tui`; exclude those areas.; The working tree contains
…[truncated]
- 2026-08-11 preload: 2 file(s), ~7 KiB seeded into implementer context
- 2026-08-11 implementer report: Completed task 0316’s risk-based Go test audit outside `internal/orchestrator`, `internal/config`, and `internal/tui`.  Changes: - Removed 437 lines of low-value test coverage across 15 test files:
…[truncated]
- 2026-08-11 review tier: high-powered — reviewers: sol, claude
- 2026-08-11 review (sol): accept — The change is a focused, production-code-free reduction of low-value tests: it removes exact prose/formatting checks, literal/default assertions, vacuous helpers, and duplicate boundary coverage while
…[truncated]
- 2026-08-11 review (claude): unknown — reviewer error: turn 1: API returned non-200 status code 429: {"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit. Please try again later.
…[truncated]
- 2026-08-11 revision: Trimmed the trailing space from task 0316’s generated implementer-report work-log line without changing the audited test cleanup. Re-staged the corrected task file. Both `git diff --check` and `git
…[truncated]
- 2026-08-11 review (sol): accept — The audited test cleanup is unchanged and remains acceptable. The original trailing whitespace was removed, but the newly appended revision work-log entry introduced another trailing space, so the met
…[truncated]
- 2026-08-11 review (claude): unknown — reviewer error: turn 1: API returned non-200 status code 429: {"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit. Please try again later.
…[truncated]
- 2026-08-11 decision: accept — commit: Prune low-value Go tests
- 2026-08-11 usage: 5,117,746 tok (in 1,099,831, out 20,219, cache_r 3,997,696, cache_w 0) · cost n/a (unpriced)
  implementer: 3,947,695 tok (in 632,849, out 11,934, cache_r 3,302,912, cache_w 0) · cost n/a (unpriced)
  coordinator: 624,617 tok (in 240,098, out 4,103, cache_r 380,416, cache_w 0) · cost n/a (unpriced)
  reviewer:sol: 545,434 tok (in 226,884, out 4,182, cache_r 314,368, cache_w 0) · cost n/a (unpriced)
