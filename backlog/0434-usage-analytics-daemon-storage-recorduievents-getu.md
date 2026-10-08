---
id: "0434"
title: 'Usage analytics: daemon storage, RecordUiEvents/GetUiAnalytics RPCs, `ycc analytics` report'
status: done
priority: 2
created: "2026-10-08"
updated: "2026-10-08"
depends_on: []
spec_refs:
    - Client interaction model
    - docs/design/usage-analytics.md#Event schema (`UiEvent`)
---

## Description
Private, daemon-owned client usage analytics so the user can see which parts of the apps they actually use (design: docs/design/usage-analytics.md).

## Acceptance criteria
- Proto: `RecordUiEvents` (batched UiEvents + client/version/visit id + optional catalog) and `GetUiAnalytics` (days/client filter → summarised report). Go/Swift/web generated code regenerated.
- `internal/uianalytics`: validated, owner-only JSONL store under `<state>/ycc/analytics/` (monthly files, 12-month retention, latest catalog per client); invalid events dropped (charset/length/attr bounds), timestamps clamped.
- Only the persistent daemon stores; in-process daemon accepts and discards (`stored=false`).
- Summary: per-client visits/active days/versions, views (count, dwell), actions (count, via breakdown, views), navigation paths, errors by code, flows (open/submit/cancel), never-used catalog entries, shortcut-not-learned.
- `ycc analytics [--days N] [--client X] [--json]` via daemon, falling back to reading the local store directly; prints the raw JSONL directory.
- Spec/doc updates; tests for store validation, retention, summarisation, RPC.

## Work log
