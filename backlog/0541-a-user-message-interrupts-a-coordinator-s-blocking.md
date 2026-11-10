---
id: "0541"
title: A user message interrupts a coordinator's blocking `wait`
status: done
priority: 2
created: "2026-11-10"
updated: "2026-11-10"
depends_on: []
spec_refs: []
---

## Description
When the coordinator is blocked in `wait` and the user sends a message, the message currently queues until the job finishes or the wait times out (default 10 min).

Acceptance:
- A user message queued during the coordinator's `wait` makes that `wait` return promptly with reports for finished jobs, the still-running ids, and a note that it returned early for the user's message. The message then enters history right after the tool result, through the normal checkpoint path.
- A message already queued when `wait` starts makes it return immediately.
- Jobs keep running, and their completion is still notified automatically later.
- Subagents' `wait` calls are unaffected.

## Acceptance criteria

## Outcome
A queued user message closes the coordinator's `tools.Workspace.InputWake` channel (`Session.inputWake`, wired via `Deps.InputWake`), which cancels the `wait` tool's wait context. Known gap: a second `wait` in the same tool-call batch still blocks, because the first checkpoint already drained the message into the batch's deferred delivery.

Commit: "Coordinator wait returns early when the user sends a message (0541)"

## Work log
