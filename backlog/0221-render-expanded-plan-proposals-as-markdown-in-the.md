---
id: "0221"
title: Render expanded plan proposals as Markdown in the TUI
status: done
priority: 2
created: "2026-07-20"
updated: "2026-07-20"
depends_on: []
spec_refs:
    - spec.md#5.2 Event shape
---

## Description
Plan proposal transcript rows should be human-readable when expanded instead of exposing a raw JSON-like payload. Render the `plan_proposed.data.plan` field through the existing Markdown renderer, keep a useful compact summary (task id + first plan line), and cover multiline/list rendering with TUI tests.

Acceptance criteria:
- Expanding a `plan_proposed` row displays only the plan text as rendered Markdown, not the event JSON envelope.
- Collapsed plan rows identify the task and summarize the plan's first non-empty line.
- Missing/malformed plan payloads degrade safely.
- Focused TUI tests pass.

## Acceptance criteria

## Work log
