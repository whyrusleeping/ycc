---
id: "0444"
title: Clarify job cancellation and cleanup in tool descriptions
status: done
priority: 4
created: "2026-10-10"
updated: "2026-10-10"
depends_on: []
spec_refs: []
---

## Description
Clarify model-facing kill_job and wait descriptions: shell termination versus subagent-run cancellation, no rollback, and killed/terminal reports versus actual execution cleanup. Follow-ups must wait until the agent has unwound; describe fresh context as preferable after cancellation. Prose-only change; preserve runtime and schema semantics.

## Outcome
Updated kill_job, wait, and send_to_agent descriptions to distinguish cancellation from completed cleanup, state that completed work is not rolled back, and recommend fresh context after cancellation. Runtime and schemas are unchanged. Existing cancellation/report/follow-up tests and diff checks passed; changes remain uncommitted.
