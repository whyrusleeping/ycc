---
id: "0527"
title: Prune low-value tests added since the August audit
status: done
priority: 3
created: "2026-11-10"
updated: "2026-11-10"
depends_on: []
spec_refs:
    - CONTRIBUTING.md#Tests
---

## Description
Go tests grew from ~20k to ~61k lines since the 0312/0316 prune (3f4d3c9); web tests (clients/web/test, ~6.8k lines) were never audited and YccKit tests (~12.6k) were last pruned in 0313. Re-apply CONTRIBUTING.md#Tests across Go, web and YccKit suites. internal/tui, internal/e2e and internal/setup are excluded: 0481 removes them with the TUI.

## Acceptance criteria
- Every removed or consolidated test is low-value by CONTRIBUTING.md#Tests (prose/literal/trivial/type-checked, or duplicated at another boundary with no independent failure mode), with a named reason.
- Credentials, authorization, persistence, concurrency, path confinement, accounting, parsing, destructive operations and observed regressions keep focused coverage.
- No production code changes; no coverage target or deletion quota.
- `go test ./...`, web `npm test`, and YccKit `swift test` pass.

## Work log
- 2026-11-10 outcome: 77 test files, −1,936/+157 lines. Removed or merged duplicate, vacuous, prose/literal, format-only and dead-code tests across Go (session, server/daemon/cmd, orchestrator, engine/tools, config/git/docs/event/sessionview, small packages), web (435→415 tests) and YccKit (571→543 tests). Security, credential, git, event-durability and regression tests were kept. An independent review found one gap (background Bash `timeout_s` bound); fixed by adding a case to TestDispatchValidatesBoundsAndBooleanWithoutExecuting. `go test ./...`, `go vet`, web `npm test`/typecheck, and `swift test` on the Mac pass. Commit: "Prune low-value tests added since the August audit".
