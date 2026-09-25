---
id: "0385"
title: Investigate vals session history for harness performance and ycc improvements
status: done
priority: 3
created: "2026-09-17"
updated: "2026-09-17"
depends_on: []
spec_refs: []
---

## Description
User-requested open-ended retrospective of vals development using ycc. Inventory session corpus, quantify observable failures/overhead, read representative successful and troubled trajectories, trace findings to current harness behavior, and write an evidence-backed report with prioritized improvements. Investigation only; proposed follow-on changes require acceptance. Avoid reproducing secrets/private transcript contents unnecessarily.

## Acceptance criteria

## Work log

- 2026-09-17: Completed full-corpus census plus independent user-experience, delegation/review, and reliability investigations. Fixed event cutoff before 2026-09-17 UTC yields 210 nonempty logs, 415,206,982 bytes, 171,405 events. Verified all 137 recorded commit SHAs exist in vals Git; distinguished outcome evidence from event-count proxies.
- Report: `docs/reports/vals-harness-retrospective.md`, including session/sequence citations, current-code checks, historical fixes, uncertainty, and prioritized evaluation gates. Reproducible read-only census: `scripts/vals-session-census.py`. Raw transcripts and credential values are not copied into deliverables.
- Filed follow-on ideas 0386–0392 as proposed only: Codex batching evaluation, readiness consistency, isolated reviewer builds, secret entry/reference workflow, memory provenance, experiment supervision, and replacement-context estimation. Existing related tasks remain in their original states; no product behavior changed.
- Independent final review corrected a source-symbol reference and narrowed revision/commit co-occurrence claims. Census hardened for RFC3339 timezone offsets and actionable incomplete-live-tail failure. Synthetic parser/cutoff/accounting checks passed; fixed-cutoff full census and aggregate SHA-256 reproduced unchanged; `git diff --check` passed. Product build/test suite not run because no product source changed. Unrelated concurrent backlog edits preserved; no commit made.
