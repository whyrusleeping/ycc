---
id: "0334"
title: Show current context tokens on iOS session usage
status: in_review
priority: 1
created: "2026-08-15"
updated: "2026-08-15"
depends_on: []
spec_refs:
    - spec.md#20.5 Surfaces
---

## Description
Surface the latest coordinator model turn's `context_tokens_est` prominently on the iOS Session usage sheet, including a clear unavailable state for sessions without telemetry. Fold the value from the session event projection, keep it live while the session runs, and add headless projection coverage.

Acceptance criteria:
- Session usage shows a top-level Current context token estimate, not merely cumulative input/output spend.
- The value comes from the newest completed coordinator `model_turn` and ignores subagent turns.
- Older/no-turn sessions degrade clearly without hiding cumulative usage.
- Swift unit tests cover folding, replacement, and subagent exclusion.
- Durable usage/session-surface behavior is documented in the spec.

## Acceptance criteria

## Work log
