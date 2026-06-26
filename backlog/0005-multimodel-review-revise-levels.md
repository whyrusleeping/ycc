---
id: "0005"
title: Multi-model review, revise loop, interaction levels (M3)
status: todo
priority: 3
created: 2026-06-25
updated: 2026-06-25
depends_on: ["0004"]
spec_refs: ["The work orchestration", "Interaction levels", "Backends & model registry"]
---

## Description
Make review genuinely multi-perspective and closed-loop, and give the user control over
autonomy. Reviewers fan out concurrently across Claude/GPT/GLM/local; the coordinator
can send revision instructions back to the implementer (reusing its context) and trigger
a re-review (reusing reviewer contexts). Add the three interaction levels.

## Acceptance criteria
- [ ] reviewer fan-out across configured `roles.reviewers`, concurrent + barrier
- [ ] send_to_implementer (reuse implementer ctx) + re_review (reuse reviewer ctx)
- [ ] coordinator judge step: accept vs revise with recorded rationale
- [ ] interaction levels interactive | judgement | autonomous gate the ask_user tool
- [ ] autonomous mode accumulates assumptions/decisions into the final report

## Work log
