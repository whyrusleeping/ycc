---
id: "0524"
title: 'Chat: hand a task to the work pipeline (review + commit) without ending the conversation'
status: done
priority: 3
created: "2026-11-10"
updated: "2026-11-10"
depends_on: []
spec_refs:
    - spec.md#9. Modes
---

## Description

**Context (2026-11-11 mode review):** what work mode adds is the pipeline (independent proportional review, backlog bookkeeping, commit), not different tools. Today the only route into it from a planning conversation is PM's `switch_to_work`, which ends the session for a fresh coordinator. Chat has no route at all: the owner either starts a work session by hand (18 user-started work sessions vs. 152 chats) or lets chat implement inline.

**Proposal:** a chat coordinator tool, e.g. `start_work(task_id, handoff)`, that starts a work session for one specific task. It carries a bounded handoff (planning context, decisions, verification expectations) the same way `switch_to_work` carries a verbatim task, and the chat stays open. It requires explicit user approval, like `switch_to_work` (a real confirmation even when autonomous). The chat gets the new session id back and can report on or await it.

**Design questions to settle:**
- Shared worktree vs. a separate workstream. Default to a workstream when the chat or other sessions have uncommitted edits in overlapping files? Reuse the 0518 "separate workstream" option.
- How completion reaches the chat: a background-job-style report vs. the user checking the session.
- Relationship to 0463 (chat commits its own finished work). Both stay: inline chat work commits per policy; the pipeline is for work you want reviewed and committed independently.

**Acceptance**
- The spec documents the tool and its approval gate.
- From a chat, an approved hand-off starts a work session on exactly that task, with the handoff in its opening context, and the chat continues. The decision is recorded in both sessions' events.
- Covered by focused orchestrator/session tests. Verified once end to end with a scripted mock model.

## Outcome

Attended chats get start_work(task_id, handoff, separate_workstream?): checks the task is actionable and not held, asks a real Confirm, then starts an attended work session pinned to the task (handoff in its opening prompt, explicit get_task preload) in the chat's worktree or a new workstream (workstream path refuses a task file that differs from the project's committed HEAD). Both sessions record a work_handoff event; the chat keeps running and tracks the work session as a "session" background job whose report (how it ended, commits, final report) arrives when it finishes/idles/errors/stops; kill_job only stops tracking. Spec §9 and remote-api.md updated. Tests: orchestrator gates/tracking/kill, session e2e with a scripted OpenAI stub, workstream cases; also verified against a real daemon with a scripted mock (.ycc/tmp/handoff-e2e/run.sh). Client-side rendering of work_handoff not added.

Commit: Chat start_work: hand one task to a new work session (user-confirmed), chat continues and gets the outcome as a background job (0524)
