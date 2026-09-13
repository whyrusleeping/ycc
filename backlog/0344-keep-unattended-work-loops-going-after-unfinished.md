---
id: "0344"
title: Keep unattended work loops going after unfinished task sessions
status: done
priority: 1
created: "2026-09-06"
updated: "2026-09-06"
depends_on: []
spec_refs:
    - 9.1 Unattended work loop
---

## Description
User reports Astra vals auto loop repeatedly stops with unfinished tasks. Latest vals loop loop_6dd0ad38 ended 'session made no progress' after s_b92adfd52ef2590d ran task 0233, committed failed hardware-gate evidence, returned task to todo, and finished. Metadata fingerprint saw unchanged task status despite substantive investigation.

Acceptance: unfinished/unchanged-backlog sessions do not terminate a work loop while ready work remains; preserve explicit stop, budget, provider failure, restart and no-ready-work boundaries. Unattended prompts must instruct continued diagnosis/revision of unmet criteria, not arbitrary round limits or treating accepted failed-test evidence as completion. Fresh sessions should receive bounded continuation context so they advance rather than blindly repeat the failed attempt. Add regression tests and update spec.

## Acceptance criteria

## Work log
- 2026-09-06: Removed the metadata-fingerprint no-progress stop without adding a session/retry limit. Fresh loop sessions now carry only bounded prior-session id/focus/report evidence, explicitly untrusted, separately recorded for replay rather than echoed as user input. Tightened direct/delegated unattended guidance to advance unmet criteria, preserve actionable accepted scope, distinguish genuine blockers, and avoid round-count in_review exits. Updated spec §9.1 and remote API text. Regression coverage includes three unchanged-metadata sessions followed by completion; unchanged sessions still honor token/cost/session budgets and graceful stop; continuation bounding, report extraction, and replay. Relevant session/orchestrator/engine/event/server/TUI tests and targeted session race tests pass. Status intentionally left unchanged for coordinator review.
