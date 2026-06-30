---
id: "0080"
title: Give capture agent bounded read access to ground backlog items in the codebase
status: done
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
- 2026-06-30 plan: Task 0080 is largely already satisfied by code committed in 8c5c72f: the capture agent (RunCapture in internal/orchestrator/capture.go) already registers the read-only Read tool plus list_backlog/get_
…[truncated]
- 2026-06-30 implementer report: Closed Task 0080 by verifying existing bounded read-access behavior and lightly tightening guidance.  Changes: 1. internal/orchestrator/capture_test.go — added TestRunCaptureGroundsInCodebase: creat
…[truncated]
- 2026-06-30 review tier: simple (coordinator self-review)
- 2026-06-30 decision: accept — commit: capture: verify bounded read-access grounding + tighten prompt (task 0080)  The capture agent already had read-only Read + list_backlog/get_task tools and a bounded prompt (from 8c5c72f). Add TestRunC
…[truncated]
