---
id: "0319"
title: 'Normal chat: spawn model-selectable subagents as reusable background jobs'
status: done
priority: 2
created: "2026-08-12"
updated: "2026-08-12"
depends_on: []
spec_refs:
    - Agent engine#Subagents and asynchronous jobs
    - Tools and access policy
---

## Description
Give the normal/direct chat agent a generic subagent tool that accepts a model and prompt, runs the agent through the session-owned background job system, and allows follow-up prompts after a completed turn while retaining that subagent's history.

## Acceptance criteria

- Normal/direct chat exposes a generic subagent spawn tool with an explicit prompt and selectable configured model.
- Generic subagent runs asynchronously under the existing session job registry and is controllable through `job_output`, `wait`, and `kill_job`.
- A completed generic subagent can receive a follow-up prompt using its retained loop/history, producing another background job.
- Generic agents are read-only by default so they can safely fan out in one worktree; chat can explicitly request mutating worker access for delegated coding, subject to the single-writer guard.
- Lifecycle/job events and replay remain valid, with tests covering spawn, model selection, completion, follow-up context, and relevant errors.
- Durable design documentation describes the capability.

## Work log
