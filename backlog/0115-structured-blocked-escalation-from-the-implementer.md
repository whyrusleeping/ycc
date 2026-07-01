---
id: "0115"
title: Structured "blocked" escalation from the implementer to the coordinator
status: todo
priority: 4
created: "2026-07-01"
updated: "2026-07-01"
depends_on: []
spec_refs:
    - 10. The `work` orchestration (in detail)
    - 11. Interaction levels
---

## Description
## Description
The implementer has no structured way to say "I'm blocked on a decision" — it can only weave it into its finish report prose, and the coordinator may or may not notice and escalate. Give the implementer a structured escape: either a `blocked(reason)` variant/field on `finish_implementation`, or a documented report convention the coordinator prompt explicitly checks for. The coordinator should then ask_user (level permitting) or mark the task blocked with the reason, rather than pushing the implementer to guess.

## Acceptance criteria
- [ ] Implementer can signal blocked-with-reason distinctly from a normal finish
- [ ] Coordinator prompt instructs how to handle it (ask_user / update_task blocked, per interaction level)
- [ ] The reason lands in the task work log
- [ ] Revise loop unaffected for normal finishes

## Acceptance criteria

## Work log
