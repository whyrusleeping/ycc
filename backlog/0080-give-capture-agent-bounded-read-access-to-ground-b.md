---
id: "0080"
title: Give capture agent bounded read access to ground backlog items in the codebase
status: todo
priority: 3
created: "2026-06-30"
updated: "2026-06-30"
depends_on: []
spec_refs: []
---

## Description
## Context

When capturing a new backlog item, the capture agent currently works mostly from the user's natural-language description. It would write better-informed, less duplicative tasks if it could lightly inspect the codebase — e.g. read relevant files, list existing backlog tasks, and check for prior art before drafting the task.

The key constraint is that this should remain a **quick capture**, not a planning/investigation session. The agent should do *just enough* reading to write a well-informed issue, without going deep or burning excessive turns.

## Scope

- Allow the capture agent to read the codebase (Read) and inspect existing backlog (list_backlog / get_task) during capture.
- Keep investigation shallow and bounded — capture stays fast; deep planning is out of scope.
- Update the capture agent's prompt/guidance to make the "minimal investigation, don't go crazy" boundary explicit.

## Acceptance criteria

- [ ] Capture agent can read files and query the backlog while drafting a new task.
- [ ] Prompt/instructions explicitly bound the investigation (quick capture, not planning; avoid duplicates; minimal reads).
- [ ] Behavior verified on a sample description: produces a well-formed task informed by relevant code without excessive tool calls.

## Acceptance criteria

## Work log
