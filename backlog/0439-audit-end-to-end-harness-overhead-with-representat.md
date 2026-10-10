---
id: "0439"
title: Audit and simplify harness startup, tool context, and job controls
status: done
priority: 3
created: "2026-10-09"
updated: "2026-10-10"
depends_on: []
spec_refs:
    - 7. Agent engine
    - 10. Work orchestration
---

## Description
Remove unused-role startup work, audit/trim tool descriptions and schemas, preload only the named task, investigate compaction and job-tool usage across projects, and simplify job controls using that evidence. Preserve independent other-model review and durability/authority/job-lifecycle safeguards. Favor smaller tasks and lower-context agents; no semantic-compaction redesign, context-estimator rewrite, or live-provider A/B was accepted.

## Acceptance criteria
- Unused subagent credentials do not block startup; invoked failures are explicit, not nil backends or accepted reviews.
- Tool definitions are smaller without losing safety, scope, defaults, or recovery guidance.
- Named-task startup contains real get_task evidence and current dependency/status readiness, not the whole backlog.
- Deduplicated cross-project evidence explains actual job usage and compaction triggers/frequency.
- Job controls are simpler while reports, notifications, owner scope, cancellation, output bounds, and replay remain truthful.
- Behavioral checks and independent other-model review are addressed.

## Outcome
Role specs are metadata-only until invoked; unavailable independent review cannot silently become self-review. Task-only startup preserves readiness through a compact get_task eligibility line. Tool descriptions/schema prose are roughly one-fifth smaller in sampled serialized toolsets. job_result is removed: job_output now peeks at terminal reports by default while explicit ranges/tails retain captured-output reads; wait still acknowledges completion. Discovery and rare but useful range/scope options remain.

The 987-session audit found two reactive coordinator rollovers, zero explicit ones, and substantially more deliberately fresh agents. Existing compaction remains a recovery backstop; no new compaction behavior was added. Full results and caveats: docs/reports/harness-usage-audit-2026-10.md. Future task-only cohorts can show whether agents manually restore list_backlog; historical preload data cannot answer that yet.

Verification: initial full Go suite passed with TestRepositoryDocsConfig excluded (its pre-existing stale expectation was subsequently confirmed in a Git-backed HEAD snapshot and corrected before commit); targeted race checks and go vet passed. Independent Opus review identified the readiness omission; fixed and re-reviewed with no remaining blocker/major. Changes remain uncommitted alongside unrelated concurrent work.
