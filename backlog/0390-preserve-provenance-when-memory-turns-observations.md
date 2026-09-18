---
id: "0390"
title: Preserve provenance when memory turns observations into durable guidance
status: done
priority: 2
created: "2026-09-17"
updated: "2026-09-18"
depends_on: []
spec_refs:
    - 6.3 Plans and memory
    - 11. Questions, unattended work, and confirmation
---

## Description

Vals user explicitly corrected an invented +/-13% run-variance rule: it was one measured observation, not a user policy (s_b44e2a8799400f69 #694/#696/#702, 2026-08-29). Memory is advisory, yet summaries can promote measurements or agent suggestions into authority across sessions.

Acceptance criteria:
- Distinguish user-stated preference/authorization, measured observation, model inference, and proposed policy in memory writes and subsequent prompt rendering.
- Attach available source session/event references and date/scope automatically rather than asking the model to fabricate provenance.
- Do not treat memory as authorization for destructive actions; real authorization and approved design remain independently sourced.
- Make corrections supersede contradicted guidance without deleting the underlying audit trail; keep memory concise rather than injecting full transcripts.
- Behavioral fixtures cover a single noisy benchmark measurement versus an actual user threshold, and a user correction followed by a fresh session. The latter must not resurrect the invented rule.

Related to context rollover authority preservation in 0357; this is the durable memory writeback boundary. See docs/reports/vals-harness-retrospective.md. Promoted to accepted work during user-authorized backlog triage; memory remains advisory, not a source of authorization.

## Outcome

Added typed advisory memory with runtime-selected candidate session/event/date/scope evidence, explicit non-authorization framing, and active-only rendering that retains superseded audit records. Active-memory budgets permit reducing corrections without audit-size lockout. Behavioral fixtures cover noisy measurements versus user thresholds, fresh-session corrections, legacy notes, and budget recovery. Both independent reviewers accepted; coordinator reran docs and focused orchestrator/session tests successfully. Full orchestrator tests passed in reviewer confinement; local sandbox-probe timeout reproduced on unchanged HEAD. Verified 56 pre-existing unrelated files unchanged.

Commit: Preserve memory provenance and supersede corrected guidance
