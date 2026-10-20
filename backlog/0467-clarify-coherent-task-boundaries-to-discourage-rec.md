---
id: "0467"
title: Clarify coherent task boundaries to discourage recursive step-sized splitting
status: done
priority: 3
created: "2026-10-17"
updated: "2026-10-17"
depends_on: []
spec_refs: []
---

## Description
Clarify task-sizing guidance following the Wheeler analysis in docs/reports/wheeler-task-granularity-2026-10-17.md. User accepted the prompt-only nudge: prefer coherent outcomes over step-sized tasks, reassess further splits against parent completion, and reuse valid closure evidence without weakening acceptance or safety gates.

## Outcome
Added shared task-sizing guidance used by work, PM and chat, made the work prompt's split instruction conditional, and clarified evidence reuse and integration checks at parent closure. Updated spec §6.2. No scheduler, tool, commit/finalization, or review-gate behavior changed.

Self-reviewed the diff; `go test ./internal/orchestrator` and scoped `git diff --check` passed. No prompt-prose tests added. Behavioral improvement remains to be observed on subsequent work; the running daemon was not rebuilt or restarted.
