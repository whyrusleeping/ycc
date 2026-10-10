---
id: "0366"
title: Make unattended resource limits explicit and expose repeated-failure evidence
status: done
priority: 2
created: "2026-09-08"
updated: "2026-10-10"
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

## Outcome

Reviewed the resource-envelope and repeated-failure evidence implementation already committed in 5a0225b: snapshot/persisted limits with explicit unbounded dimensions, cost caps counting priced usage only, per-attempt evidence including failures with no idle report, actionable unfinished digest rows, and the RPC, TUI, web and iOS displays. Fixed the session-budget halt instruction, which told agents to move unfinished work to in_review/blocked; it now keeps that work todo/in_progress with evidence, remaining criteria and the next step. Added tests for repeated failed experiments carrying failure context into the next session and for the halt instruction, and aligned spec §20.6. No new default limits or attempt-count exit were added. go test ./... passes, session tests pass under -race, and the web workloop tests pass. iOS was inspected only, because no Swift toolchain is available.

Commit: session: budget wrap-up keeps unfinished work actionable; test repeated failed experiments (0366)
