---
id: "0005"
title: Multi-model review, revise loop, interaction levels (M3)
status: done
priority: 3
created: 2026-06-25
updated: 2026-06-25
depends_on: ["0004"]
spec_refs: ["The work orchestration", "Interaction levels", "Backends & model registry"]
---

## Description
M3 is the multi-model review milestone in the original spec roadmap (spec.md at 7365ee4; current spec.md no longer lists milestones).

Make review genuinely multi-perspective and closed-loop, and give the user control over
autonomy. Reviewers fan out concurrently across Claude/GPT/GLM/local; the coordinator
can send revision instructions back to the implementer (reusing its context) and trigger
a re-review (reusing reviewer contexts). Add the three interaction levels.

## Acceptance criteria
- [x] reviewer fan-out across configured `roles.reviewers`, concurrent + barrier
- [x] send_to_implementer (reuse implementer ctx) + re_review (reuse reviewer ctx)
- [x] coordinator judge step: accept vs revise with recorded rationale
- [x] interaction levels interactive | judgement | autonomous gate the ask_user tool
- [x] autonomous mode accumulates assumptions/decisions into the final report

## Outcome

Implemented the model/role registry, concurrent multi-model reviewer barrier, context-preserving implementer revision and re-review loop, structured questions, and interactive/judgement/autonomous behavior with assumption capture. Race-tested scripted coverage verified revision reuse and interaction paths; a live autonomous Claude run fanned out two reviewers concurrently and completed acceptance.

Commit: ad1ee9e — Build out the docs-driven harness: M0–M4, single binary, chat mode, TUI

## Work log
- 2026-11-14 groom: glossed M3 and checked off the criteria on this done task. The Outcome and ad1ee9e (internal/orchestrator has send_to_implementer and re_review, and interaction levels gate ask_user) show them met.
