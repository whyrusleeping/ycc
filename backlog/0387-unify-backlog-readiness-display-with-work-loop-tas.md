---
id: "0387"
title: Unify backlog readiness display with work-loop task eligibility
status: done
priority: 2
created: "2026-09-17"
updated: "2026-09-18"
depends_on: []
spec_refs:
    - 6.2 Backlog
    - 9.1 Unattended work loop
---

## Description

Vals retrospective: session s_e2479ddba5f75449 event 12 (2026-09-05) reports that all seven tasks displayed READY are actually blocked. Current orchestrator list_backlog marks dependency-clear blocked tasks READY, while workloop only schedules todo/in_progress. Distinguish dependency satisfaction from actionable readiness using shared semantics rather than prompting agents to reinterpret contradictory labels.

Acceptance criteria:
- A shared task-eligibility contract drives list_backlog and work-loop selection, preserving any explicitly documented ordering differences.
- Blocked tasks can say dependencies satisfied but never appear under Ready to start solely for that reason; proposed and in_review tasks retain deliberate eligibility rules.
- Human-readable and structured output identify blocker/unblock requirements without treating a missing dependency as resolved.
- Focused tests cover dependency-clear blocked tasks, actionable in_progress continuations, proposed tasks, missing dependencies, and an all-blocked backlog.
- Do not auto-unblock tasks or weaken external authorization/resource gates.

Evidence: docs/reports/vals-harness-retrospective.md. Complements preflight task 0324. Promoted to accepted work during user-authorized backlog triage; preserve external blockers and authorization gates.

## Outcome

Added shared eligibility semantics for backlog display and work-loop selection while preserving display and priority ordering. Only dependency-clear todo/in_progress tasks are actionable; blocked, proposed, and in_review gates remain intact. Human-readable and structured views distinguish missing/unfinished dependencies and explicit unblock requirements. Focused status/dependency regression tests and go test ./... passed; independent review accepted. Unrelated workspace changes preserved.

Commit: Unify backlog readiness with work-loop task eligibility
