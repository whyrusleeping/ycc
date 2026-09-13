---
id: "0366"
title: Make unattended resource limits explicit and expose repeated-failure evidence
status: blocked
priority: 2
created: "2026-09-08"
updated: "2026-09-10"
depends_on: []
spec_refs:
    - §9.1 Unattended work loop
    - §11 Questions, unattended work, and confirmation
---

## Description
Long-running unattended work should have a clear resource envelope and actionable repeated-failure diagnostics while retaining the newly accepted keep-diagnosing continuation behavior. This is not the arbitrary review/session-count circuit breaker proposed in 0322; do not automatically accept that older policy.

## Acceptance criteria
- At loop start and in status/digest, make configured token/cost/time limits and intentionally unbounded dimensions explicit; account honestly for unpriced models.
- Expose repeated failures/attempts with task focus, latest evidence, remaining criteria, and useful next steps, including failure reports when no session_idle report exists.
- Respect explicit stops and budget wrap-up, leaving unfinished accepted work actionable or genuinely blocked.
- Do not infer no progress solely from absent commits/status changes or impose a fixed three-round exit; preserve valid diagnosis and continuation.
- If new default limits or an escalation policy would change existing unattended behavior, evaluate/document the tradeoff and obtain explicit policy approval rather than silently installing it.
- Tests cover bounded/unbounded settings, unpriced usage, repeated failed experiments, useful non-committing progress, and failure-context continuation.

## Plan

Expose the existing resource envelope (loop and session caps, explicitly unbounded time/dimensions, priced-only cost semantics) in durable loop snapshots and user-facing status/digest without introducing new limits. Capture bounded per-attempt evidence, including session errors without idle reports; surface task focus/attempt counts and actionable remaining-work context while preserving diagnosis continuation and explicit stop/budget behavior. Add targeted behavioral coverage and update existing operation/spec text where needed. Verify daemon/RPC/client projections and generated Go/Swift artifacts if protocol fields change. Review independently and selectively commit only this task, preserving the pre-existing dirty tree.

## Work log

- 2026-09-10: Implementation is present but NOT reviewed or committed. Added resource-envelope capture/persistence and legacy-unknown handling, priced-only cost disclosure, bounded attempts/error-without-idle evidence, unfinished/actionable digest rows, RPC/protobuf projections, TUI/iOS displays, and regression coverage. No new default limits or attempt-count exit. Implementer reported `go test ./...` and targeted session race tests passing; coordinator confirmed `go test ./internal/session ./internal/server ./internal/tui` and `git diff --check`. Go/Swift protobufs regenerated; no Swift toolchain available. Existing buf lint Event-reuse findings remain.
- Review/finalization is blocked by the session baseline ownership guard, not by a test failure: `spawn_reviewers` refuses overlap with pre-dirty workloop, persistence, server, TUI, proto/generated, spec, and remote-api paths (baseline `0a3b09d571da0dc6908a29358cfb6e2aa1bcd8fe55b8c56a513c3488b49c4e1a`). No exposed tool can move this session's baseline/workspace. Unblock with a clean isolated worktree/session and explicitly owned task patch; do not bypass the guard or commit all dirty files.
- Handoff: unrelated staged index was confirmed byte-for-byte unchanged before this log update (SHA-256 `06dd8adbbc237619c3c4a7c751d604152d616b170bbe7c0450b30503ecefdd11`). All task code is the unstaged diff plus untracked `internal/session/workloop_resources_test.go`; initial partial task changes are included. Backups at `/tmp/ycc-0366-baseline/`: `index.diff` is unrelated staged baseline (also contains this task's original backlog entry), `tree.tar` initial working files, `unstaged.diff` initial partial task, `completed-task-over-index.diff` final task code, and a copy of the new test. Detailed verification/report is in `.ycc/sessions/s_33569fea07f3b4e7/events.jsonl`. Next: assemble task-only changes on HEAD in an isolated worktree, account for dependencies on staged continuation work (0344), regenerate rather than interdiff generated protos, verify candidate, independently review, then scoped commit. Acceptance remains unconfirmed until that review and isolated verification; preserve existing valid work.
