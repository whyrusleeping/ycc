---
id: "0002"
title: Core agent loop with worker tools (M0 spike)
status: done
priority: 1
created: 2026-06-25
updated: 2026-06-25
depends_on: ["0001"]
spec_refs: ["Agent engine", "Tools"]
---

## Description
M0 is the engine-spike milestone in the original spec roadmap (spec.md at 7365ee4; current spec.md no longer lists milestones).

The atom of the system: a `Loop` that runs turns against a gollama client, dispatches
tool calls, feeds results back, and terminates when the model yields with no tool call
or calls a control tool (`finish`). Includes the first worker tools so a single agent can
do a real coding task end-to-end. Events to stdout for now.

## Acceptance criteria
- [x] `Loop{client, model, system, tools, history, events}` with a `run()` driver
- [x] worker tools: read_file, write_file, edit_file, list_dir, grep, glob, bash
- [x] control-tool concept (a tool may end the loop / change state)
- [x] demo: point the loop at a scratch repo and have it complete a small task
- [x] every turn / tool call / result emits a structured event

## Outcome

Implemented the core agent loop, structured event emission, confined worker/control tools, and the backend-selectable spike, with scripted engine/tool coverage. Live Claude runs completed and verified Fibonacci and file-writing tasks; the run also found and fixed the missing default Anthropic version header.

Commit: 7365ee4 — ycc: initialize workspace

## Work log
- 2026-11-14 groom: glossed M0 and checked off the criteria on this done task. The Outcome and 7365ee4 (internal/tools defines read_file, write_file, edit_file, list_dir, grep, glob, bash and finish) show them met.
