---
id: "0523"
title: Tune PM onboarding into an interactive learn-the-project, build-the-backlog session
status: todo
priority: 2
created: "2026-11-10"
updated: "2026-11-10"
depends_on: []
spec_refs:
    - spec.md#9. Modes
    - spec.md#19. Onboarding
---

## Description
**Owner direction (2026-11-11):** the one place PM is clearly worth having is new-user / new-project onboarding: go learn about the project, then work with the user interactively to build out the backlog. 0521 makes that PM's only user-facing entry. This task makes the session itself good at that job.

**Current state:** `onboardPresetPrompt` (internal/orchestrator/prompts.go) branches greenfield vs. brownfield. For brownfield it says to "ask what work the user wants first, investigate that slice, seed only its docs and tasks" and offer a `switch_to_work` handoff. It is a refresh-the-docs procedure more than a guided conversation.

**Desired flow** (refine during implementation):
1. Learn first: read the README/docs/spec/backlog, the code layout, recent git history and open TODOs. Summarize back what the project is, its state, and where work seems active, and ask the user to correct it.
2. Ask about goals, priorities and constraints with `ask_user`, using concrete options grounded in what it found, in small batches rather than one long questionnaire.
3. Draft the backlog with the user. Propose candidate tasks in batches. File what the user accepts as `todo` and leave maybes as `proposed`, sized as coherent, independently verifiable outcomes. Seed or refresh only the spec sections the accepted work needs.
4. Finish with a short recap: what was recorded, and what's ready to start. Then offer, without auto-starting, to kick off the first task (via `switch_to_work`, the work loop, or the chat→work handoff if that task has landed).
- Also works when re-run on an already-onboarded project ("plan the backlog with PM" entry from 0521): start from the existing backlog and refresh it rather than re-seeding.
- Keep PM's no-code boundary; it suits onboarding.

**Acceptance**
- The onboard prompt and spec §9/§19 describe this flow. Prompt guidance stays lean, per existing prompt conventions.
- Live runs with a real model on (a) a brownfield repo with no ycc docs and (b) an already-onboarded repo. The session summarizes the project, asks grounded questions, files accepted tasks as todo and ideas as proposed, and doesn't spec the whole repo. Record transcript evidence in the work log.
- No regression to the Get started checklist's "onboard it" step (0505).

## Acceptance criteria

## Work log
