---
id: "0312"
title: Reduce low-value Go tests in orchestrator, config, and TUI
status: done
priority: 3
created: "2026-08-10"
updated: "2026-08-11"
depends_on:
    - "0310"
spec_refs:
    - CONTRIBUTING.md#Tests
    - The work orchestration (in detail)
---

## Description
Pilot a risk-based test-suite reduction in internal/orchestrator, internal/config, and internal/tui. Remove or consolidate tests of incidental prose, literal defaults, trivial accessors, exact formatting, and behavior duplicated at several layers. Preserve tests protecting real boundaries and observed regressions.

## Acceptance criteria
- Each retained test in the touched areas protects a plausible regression at the lowest useful boundary.
- Duplicate default/getter/formatting cases are consolidated where one behavioral test provides the same protection.
- TUI coverage favors state transitions and a small representative rendering set over exhaustive incidental text/spacing assertions.
- Credential handling, permissions, persistence, validation, mode/tool authorization, question/input safety, and replay behavior remain protected.
- No coverage percentage or deletion quota is used as an acceptance gate.
- The full Go test suite passes, with any existing flaky failure reported rather than papered over.

## Plan

1. Inventory tests in internal/orchestrator, internal/config, and internal/tui by the behavior or failure boundary they protect, accounting for the existing uncommitted dependency work in the tree.
2. Remove tests that only pin prose, literal defaults, trivial getters, incidental exact formatting/spacing, or duplicate behavior already protected at a lower useful boundary. Consolidate repetitive table cases where a representative behavioral test provides equivalent protection.
3. Retain coverage for credential handling, permissions, persistence, validation, mode/tool authorization, question/input safety, replay behavior, meaningful state transitions, and observed regressions; keep a small representative set of TUI rendering tests where rendering itself is the contract.
4. Run gofmt on touched Go tests, then targeted tests for internal/orchestrator, internal/config, and internal/tui. Run the full Go test suite and report any pre-existing flaky failures without weakening tests to hide them.
5. Review the final diff against the risk-based standards and confirm no production behavior changed.

### Starting points
- CONTRIBUTING.md#Tests defines the risk-based standard for this cleanup.
- The worktree already contains uncommitted 0310/0311-related changes; preserve them and keep this task's edits confined to test cleanup plus backlog metadata.
- Audit *_test.go under internal/orchestrator, internal/config, and internal/tui; production behavior should remain unchanged.

## Work log
- 2026-08-10 plan: 1. Inventory tests in internal/orchestrator, internal/config, and internal/tui by the behavior or failure boundary they protect, accounting for the existing uncommitted dependency work in the tree. 2.
…[truncated]
- 2026-08-10 context hints: 3 recorded with plan
- 2026-08-10 context hints: CONTRIBUTING.md#Tests defines the risk-based standard for this cleanup.; The worktree already contains uncommitted 0310/0311-related changes; preserve them and keep this task's edits confined to test
…[truncated]
- 2026-08-10 preload: 2 file(s), ~6 KiB seeded into implementer context
- 2026-08-10 completed: consolidated config persistence coverage into one representative round trip; removed tests of prompt prose, literal defaults, trivial getters/formatters, duplicate rendering, and incidental TUI styling while retaining security, validation, authorization, state-transition, input-safety, replay, accounting, and regression boundaries.
- 2026-08-10 implementer report: Completed task 0312 and marked its backlog item done.  Changes: - Removed 934 lines of low-value tests across `internal/orchestrator`, `internal/config`, and `internal/tui`; no production code was cha
…[truncated]
- 2026-08-10 review tier: high-powered — reviewers: sol, claude
- 2026-08-10 review (sol): revise — The cleanup is generally well targeted: prompt-prose, literal/default, trivial formatting/getter, and duplicate rendering tests were reduced; mode/tool boundaries, permissions, validation, question/in
…[truncated]
- 2026-08-10 review (claude): accept — The change is a well-scoped, risk-based test reduction confined to *_test.go under internal/config, internal/orchestrator, and internal/tui (the other diff hunks — CONTRIBUTING.md, prompt/comment cl
…[truncated]
- 2026-08-10 revision: Addressed all review findings without restoring broad duplicate coverage: - Added a representative inline `Notify.Auth` value alongside `AuthEnv` in `TestSaveRoundTrip`, so both TOML mappings are exer
…[truncated]
- 2026-08-10 review (sol): accept — The revision resolves the credential-persistence gap by including a representative inline `Notify.Auth` value in the consolidated Save/Load round trip, while keeping the test cleanup lean; the mislead
…[truncated]
- 2026-08-10 review (claude): accept — The revision cleanly resolves all prior findings: dangling/mangled doc comments left behind by deleted tests in internal/tui/question_test.go and internal/tui/settings_test.go are gone, and the consol
…[truncated]
- 2026-08-11 decision: accept — commit: Prune low-value Go tests
