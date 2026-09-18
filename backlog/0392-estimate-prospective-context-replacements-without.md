---
id: "0392"
title: Estimate prospective context replacements without the old history's measurement anchor
status: done
priority: 2
created: "2026-09-17"
updated: "2026-09-18"
depends_on: []
spec_refs:
    - 7.1 Provider boundary and history
    - 7.2 Loop, repair, and failure handling
---

## Description

Live vals rollover s_3770be49aaf4e538 #15392 records new_context_tokens_est=0 for an ~18.7 KB nonempty summary; #15393 successfully runs with a ~14,099-token estimate. Current Loop.ContextTokensEstimateForHistory calls contextEstimateForHistoryLocked, which applies prior.Measured + replacement.RawTokens - prior.RawEstimate and clamps negative values to zero. SetHistory resets lastInput only after the rollover event, so the prospective estimate still uses an incompatible old-history calibration.

Acceptance criteria:
- Estimate discontinuous candidate history with a complete provider-shaped estimate not an additive growth anchor tied to the old view; retain useful measured anchoring for ordinary append-only growth.
- Define comparable old/new estimates for the strictly-smaller rollover check and preserve fail-closed persistence/authority behavior.
- Regression test a large old history whose measured/raw sizes differ and a small nonempty replacement; assert plausible positive replacement size and truthful persisted telemetry. Verify live SetHistory and reopen remain consistent.
- Cover non-smaller replacement rejection and provider/model changes; do not silently raise context limits or add automatic rollover policy.

Related existing 0356 accounting and 0357 rollover. Evidence: docs/reports/vals-harness-retrospective.md. No product fix was applied during the investigation. Promoted to accepted work during user-authorized backlog triage; automatic rollover policy and context-limit changes remain out of scope.

## Outcome

Prospective history estimates now use complete provider-shaped accounting without old-history measurement offsets. Rollover compares and persists unanchored old/new estimates under the same selected model/provider while ordinary append growth retains measured anchoring and persistence/authority boundaries remain unchanged. Regression coverage verifies positive replacement telemetry, SetHistory/replay consistency, non-smaller rejection, and provider/model changes. Independent reviewer accepted and reported full go test ./... passing; coordinator confirmed engine/session tests and diff check. Unrelated baseline work preserved.

Commit: Estimate context replacements independently of prior measurements
