---
id: "0213"
title: Normalize orphaned persisted session status in history
status: done
priority: 2
created: "2026-07-16"
updated: "2026-07-16"
depends_on: []
spec_refs:
    - Session & event log
---

## Description
ListSessionHistory currently replays an abruptly-ended persisted log as `running` even when no live daemon session exists, causing clients such as iOS to show stale green Running badges.

## Acceptance criteria
- Persisted-only session summaries whose reduced status is `running` are reported as `stopped`.
- Truly live sessions continue to report their in-memory status, including `running`.
- Tests cover both orphan normalization and live override behavior.

## Work log
