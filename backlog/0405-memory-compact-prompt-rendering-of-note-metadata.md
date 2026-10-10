---
id: "0405"
title: 'Memory: compact prompt rendering of note metadata'
status: done
priority: 2
created: "2026-09-30"
updated: "2026-10-10"
depends_on: []
spec_refs:
    - docs/design/project-memory.md#Write and grooming policy
---

## Description
Per-note provenance boilerplate dominates active memory (~190 B per typed note, ~58 B per legacy note; ~2.9 KB of ycc's 10 KB). Replacing a legacy note 1:1 with a typed one GROWS active memory, so agents' consolidation attempts get refused (valstore s_bac96d2abad73ad8).

Acceptance:
- State the advisory/model-classified/candidate-evidence disclaimer once (prompt header), not per line.
- Typed lines render compactly, e.g. `- [observation · 2026-09-24 · s_…#442 · m-…] note`; default scope omitted; legacy lines `- [legacy-…] note` with the header explaining legacy ids are unverified.
- Budgets measure the compact render; design doc updated.

## Acceptance criteria

## Work log
