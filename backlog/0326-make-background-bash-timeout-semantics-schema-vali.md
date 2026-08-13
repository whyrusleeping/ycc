---
id: "0326"
title: Make background Bash timeout semantics schema-valid
status: done
priority: 3
created: "2026-08-12"
updated: "2026-08-13"
depends_on: []
spec_refs:
    - §7.3 Subagents and asynchronous jobs
    - §8 Tools and access policy
---

## Description
Eliminate the recurring invalid combination of `run_in_background:true` with `timeout_s`. Express foreground and background execution as mutually valid argument shapes in the tool schema, or reinterpret timeout as a job-runtime limit for background commands. Keep the existing guidance against immediately backgrounding and waiting.

Acceptance criteria:
- The provider-visible JSON schema cannot naturally produce the currently rejected foreground-timeout/background combination, or the combination has well-defined job timeout behavior.
- Existing foreground timeout behavior is unchanged.
- Background jobs still integrate with wait/job_output/kill and single-writer classification.
- Tests cover schema validation and runtime behavior for both forms.

## Acceptance criteria

## Plan

1. Change Bash's argument/runtime contract so `timeout_s` has valid, documented semantics in both foreground and background modes, preserving the existing foreground default, bounds, and process-tree termination behavior.
2. For background commands that specify `timeout_s`, enforce it as a total job-runtime limit while retaining the existing unbounded-until-killed behavior when omitted; ensure timeout finalization produces a clear terminal report/event and remains compatible with wait, job_output, kill_job, and mutating-job classification.
3. Update the provider-visible Bash schema descriptions and runtime validation so the formerly rejected combination is accepted and unambiguous, while keeping the guidance against backgrounding merely to wait immediately.
4. Add focused tests for the advertised schema/bounds, unchanged foreground timeout behavior, successful ordinary background execution, timed background termination/reporting, and existing job controls/classification. Run the relevant tools/jobs test packages and broader tests as practical.

### Starting points
- internal/tools/worker.go: bash schema, bashCall, startBackgroundBash
- internal/tools/jobs_test.go: background Bash integration tests
- internal/tools/worker_test.go: foreground timeout/schema tests
- The worktree contains unrelated uncommitted task 0328/iOS and orchestrator changes; do not modify or stage them.

## Work log
- 2026-08-13 plan: 1. Change Bash's argument/runtime contract so `timeout_s` has valid, documented semantics in both foreground and background modes, preserving the existing foreground default, bounds, and process-tree 
…[truncated]
- 2026-08-13 context hints: 4 recorded with plan
- 2026-08-13 context hints: internal/tools/worker.go: bash schema, bashCall, startBackgroundBash; internal/tools/jobs_test.go: background Bash integration tests; internal/tools/worker_test.go: foreground timeout/schema tests; Th
…[truncated]
- 2026-08-13 preload: 4 file(s), ~27 KiB seeded into implementer context
- 2026-08-13 implementer report: Implemented Task 0326 in `internal/tools/worker.go` and `internal/tools/jobs_test.go`.  Changes: - Defined `timeout_s` consistently as a command runtime limit for both execution modes. - Preserved for
…[truncated]
- 2026-08-13 review tier: single-opus — reviewers: sol
- 2026-08-13 review (sol): accept — Task 0326 is correctly implemented. `timeout_s` now has an explicit, provider-visible runtime-limit meaning for both foreground and background Bash; omitted background timeouts remain unbounded, while
…[truncated]
- 2026-08-13 decision: accept — commit: Define background Bash timeout runtime semantics
