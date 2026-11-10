---
id: "0512"
title: Agents ask end-of-turn decisions through ask_user with options, not in report prose
status: done
priority: 2
created: "2026-11-09"
updated: "2026-11-10"
depends_on: []
spec_refs:
    - spec.md#11. Questions, unattended work, and confirmation
---

## Description

**Evidence (2026-11-09 usage analysis, all projects):** about 14% of the owner's follow-ups are bare go-aheads ("yes", "continue", "go ahead", "do it"). 30 of the 69 sampled go-aheads came after a report that ended with a prose question ("Should I go ahead?", "Want me to deploy it?"), and so did 26 of 104 "commit" replies. A prose question doesn't mark the session needs-answer, doesn't trigger a question notification, and has to be answered by typing. On iOS the owner answers real ask_user questions mostly by tapping an option (17 of 23 single answers).

**Change:** chat and work prompts say that when a turn ends on a yes/no or pick-one decision the user must make before work continues, the agent asks it with ask_user and concrete options. The report should not end with a question. Open-ended "anything else?" endings are dropped. Unattended modes keep their existing system-assumption rules.

**Acceptance:**
- Prompt text and the spec section on questions are updated.
- A behavioural model eval or scripted-model test covers a turn that finishes needing a go-ahead, and shows it produces a question_asked with options rather than a trailing prose question.
- No change to how unattended or work-loop sessions answer automatically.

## Outcome

The chat, pm and work prompts now share decisionGuidance: when a turn can't continue without the user's yes/no or pick-one decision, the agent asks with ask_user and concrete options, and doesn't end on a prose question, a "say the word" offer or "anything else?". Spec §11 records this. Unattended handling is unchanged. A new opt-in live check, TestLiveDecisionAsksWithOptions (-tags liveeval, documented in docs/model-ux-evaluations.md), uses the production chat prompt. Before the change, 3 of 8 decision runs on claude and sol asked with options; after it, 12 of 12 runs passed, including control turns with no question.

Commit: Prompts: ask end-of-turn decisions via ask_user with options, not prose questions (0512)
