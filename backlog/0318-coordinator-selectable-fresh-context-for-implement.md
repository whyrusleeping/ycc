---
id: "0318"
title: Coordinator-selectable fresh context for implementation and review revisions
status: done
priority: 2
created: "2026-08-12"
updated: "2026-08-12"
depends_on: []
spec_refs:
    - §7.3 Subagents and asynchronous jobs
    - §10 Work orchestration
---

## Description
Add an explicit coordinator choice to retain or replace subagent history on subsequent implementation and review rounds. Small, localized revisions should reuse the existing implementer/reviewer loops; broad revisions, context-length failures, major approach changes, or heavily accumulated tool/review history should be handed to fresh loops with a compact self-contained handoff.

Acceptance criteria:
- `send_to_implementer` supports an optional context mode with retained context as the backward-compatible default and a fresh mode that replaces the implementer loop while preserving the task, model/role settings, workspace/tool policy, and current working tree.
- `re_review` supports the same choice; fresh mode recreates the same resolved reviewer slots/focuses, preloads the current bounded diff, and does not silently re-resolve a changed review tier.
- Fresh revision prompts include the task plus a concise coordinator-supplied handoff (findings, changed approach, and required verification) without replaying prior subagent history.
- Coordinator and tool prompting clearly explains when retained context is cheaper and more effective versus when fresh context is warranted; a context-length failure is an explicit strong signal to reset.
- Subagent outcomes expose enough compact context pressure information (at least revision count and an approximate retained-context size, when available) for the coordinator to make the choice rather than guess.
- Reset/replacement is recorded observably in subagent lifecycle events and works for foreground and completed-background implementers/reviewers without violating the single-writer rule.
- Tests cover retained behavior, fresh implementer replacement, fresh same-slot reviewer replacement, context-length recovery path, bounded diff/handoff seeding, and background-job guards.
- The spec documents selective context retention/replacement in the subagent and work-orchestration sections.

## Acceptance criteria

## Work log
