---
id: "0349"
title: Add bounded structured code search with explicit scope and continuation
status: done
priority: 2
created: "2026-09-08"
updated: "2026-09-09"
depends_on: []
spec_refs:
    - §8 Tools and access policy
---

## Description
The review's latest-50-session sample found approximately 39% of Bash calls began with rg. Shell search lacks uniform pagination, output budgets, and search-mode semantics, and adds quoting failures. Add a small first-class textual search surface while retaining Bash as an escape hatch; do not start with semantic indexing or an LSP subsystem.

## Acceptance criteria
- Search supports explicit pattern, path/glob scope, literal versus regex mode, match/file/count output, nearby context, and bounded continuation.
- Results include stable path/line references, clear no-match/error distinction, truncation metadata, and a usable next-page/range operation.
- Sensible defaults exclude generated/session data and respect repository ignore rules; intentional overrides are explicit.
- Backend availability and invalid regex/argument errors are actionable; avoid silently changing search semantics.
- Tests cover quoting-sensitive patterns, ignored files, Unicode, scoped searches, no matches, large results, pagination, and cancellation.
- Compare representative model search tasks against Bash+rg for correctness, round trips, and output volume; document the chosen compact schema.

## Outcome
Added ripgrep-backed Search to editing and inspection toolsets with explicit scope/mode/output, nearby context, ignore overrides, bounded records, deterministic continuation, and actionable errors/cancellation. Spec §8 documents the compact schema and reproducible Bash+rg comparison; comparisons use deterministic tool fixtures, not live-model evaluation. Independent review accepted after fixing continuation past offset 100000. `go test ./...` passed on the isolated task-only tree; unrelated workspace changes were preserved.

Commit subject: Add bounded structured Search with explicit scope and continuation
