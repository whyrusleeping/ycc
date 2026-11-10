---
id: "0524"
title: 'Chat: hand a task to the work pipeline (review + commit) without ending the conversation'
status: proposed
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

## Acceptance criteria

## Work log
- 2026-11-10: Set aside by the owner; reverted. It was implemented and committed as d6a1b3f: a `start_work` chat tool with user confirmation, an attended work session pinned to the task, the result tracked as a chat background job, and a `work_handoff` event. The owner then questioned whether the use case was real. No session shows anyone trying to hand work off from a chat; chat already commits its own work and can bring in a reviewer with `spawn_agent`; and starting a work session from a task in the web or iOS app is already quick. The tool also added to every chat request (24 tools, ~20 KB of definitions, see 0538). Reverted and moved back to proposed. Reopen only with a concrete case where handing off from a chat beats starting a work session from the task. The code is in git history (d6a1b3f).
