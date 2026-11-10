---
id: "0538"
title: 'Consolidate coordinator tools: fewer, merged tools per mode based on actual usage'
status: todo
priority: 3
created: "2026-11-10"
updated: "2026-11-10"
depends_on: []
spec_refs:
    - spec.md#9. Modes
---

## Description
## Why
On 2026-11-10 the owner raised a concern about tool proliferation after 0524 added `start_work` to chat. The tool definitions sent on every coordinator request were measured with `BuildMode` and every optional hook set:

| mode | tools | tool definition size |
|---|---|---|
| chat | 24 | ~20.4 KB |
| work | 25 | ~20.7 KB |
| pm | 22 | ~16.5 KB |

Each tool adds tokens to every request and one more option the model weighs on every turn. Tools have been added one at a time (recently `schedule_watch` and `start_work`) with no review of the whole set. The owner accepted a separate cleanup task instead of reverting 0524.

## Candidates (unverified; usage data decides)
- `start_work` (chat) and `switch_to_work` (pm) both hand a task to work. Use one hand-off tool for both modes.
- `remember` and `forget` → one memory tool.
- `list_jobs`, `job_output`, `wait`, `kill_job` (plus `tool_output`) → fewer job tools.
- `list_backlog` and `get_task`.
- Hooks that add a tool plus a paragraph of system-prompt guidance (`schedule_watch`, `start_work`). Check whether the guidance can be shorter or the tool offered only when relevant.

## Approach
1. Measure first. Mine `~/code/*/.ycc/sessions/*/events.jsonl` for `tool_call` counts per tool and mode. Deduplicate symlinked workspaces with realpath, and exclude wheeler's 2026-10-12 runaway work loop. Report rarely or never used tools and tools that are usually called together.
2. Propose the merges and removals to the owner, with the numbers, before changing anything.
3. Implement the accepted set:
   - Keep the behavior the merged tools covered, or drop it on purpose.
   - Update the prompts that name old tools, spec §7/§9, and any client rendering keyed on tool names.
   - Replay of old event logs must keep working: old tool names in history still render and reopen.

## Acceptance criteria
- A usage report covering every tool per mode (calls, sessions using it) is recorded in this task's work log.
- The owner has approved the consolidation list before implementation.
- After the change, each mode's tool count and definition size are recorded beside the baseline above, and both are lower.
- Existing sessions that used removed or renamed tools still reopen and replay (test).
- `go test ./internal/...` passes. Prompts and spec reference no removed tool names.

## Work log
