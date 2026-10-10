---
id: "0357"
title: Add replayable coordinator context rollover and safe overflow recovery
status: done
priority: 1
created: "2026-09-08"
updated: "2026-10-10"
depends_on:
    - "0356"
spec_refs:
    - §5 Event log
    - §7.1 Provider boundary and history
    - §9.1 Unattended work loop
---

## Description

Coordinator history has no rollover path: context-length failure parks the session and can stop an unattended batch, while retrying the unchanged request cannot succeed. Extend context recovery to the longest-lived agent, complementing subagent rollover in 0321 and output reduction in 0323. Evidence: internal/engine/loop.go:801–810; session/workloop error handling.

## Acceptance criteria

- Support explicit rollover at a safe checkpoint and a bounded safe recovery from actual overflow; never blindly resend the same oversized history.
- Preserve user intent/authorization, decisions, unresolved criteria, verification evidence, artifact references, and active job identities/ownership.
- Record context-view transitions durably so reopen reconstructs exactly the selected model view; original event history remains intact and summaries remain labeled evidence, not new user instructions.
- Handle pending tool results/questions, media/provider continuation state, model switching, and cancelled runs without duplicate mutation or fabricated completion.
- Attended clients expose a meaningful recovery action; unattended execution may continue in a fresh context subject to budgets and genuine blockers rather than treating overflow as an unrecoverable batch failure.
- Regression tests cover long coordinator histories, crash/reopen after rollover, failed summarization, and overflow during unattended work.

## Outcome

All three remaining blockers from the 2026-09-09 review are fixed, with regression tests. (1) Reopen and live sessions honour the durable switch_model gate: the oversized view is not resent, SendInput and plain Resume are refused, and an actual coordinator switch clears the gate and retries automatically through a latched, lost-wakeup-safe signal. (2) Input accepted while automatic summarization fails, or while the compact request overflows, is durably retained in the view without another request, and the owner parks with running=false. (3) Media detection is structured, covering live and reopened sessions. Also: pause during overflow is honoured, cancellation leaves no gate, the gate is keyed to the coordinator (fail-closed for legacy logs), and the summary keeps budget wrap-up and job reports. Two-reviewer comprehensive review (claude, sol) accepted. The session package passes under -race; a pre-existing workloop Notify race is unrelated.

Commit: session: honour durable context model-switch gate on reopen, retain gated input, structured media detection (0357)
