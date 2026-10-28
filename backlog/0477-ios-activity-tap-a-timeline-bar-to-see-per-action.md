---
id: "0477"
title: 'iOS Activity: tap a timeline bar to see per-action timing (model turns, tool calls)'
status: in_review
priority: 2
created: "2026-10-22"
updated: "2026-10-22"
depends_on:
    - "0475"
spec_refs: []
---

## Description
## Why
The user wants to tap things in the iOS Activity timeline and see what took so long, per agent action.

## Approach
- Daemon: index model_turn/tool_call/tool_result timing metadata for every actor (coordinator included) in the session-view activity index; new `GetActivitySteps(project, session_id, actor, since, until, limit)` RPC returns chronological steps (kind model|tool, name, bounded argument hint, status, start/finish, duration, output tokens, session-view row id) plus per kind+name totals over the whole range.
- iOS: timeline bars (and the agent detail's Rounds rows) are tappable. An agent round or the coordinator bar opens a time breakdown: wall/model/tool/other summary, slowest kinds, and a step list sortable by time or duration. A step with a row id opens its transcript row (args/output). A running round shows the in-flight step live.

## Acceptance criteria
- Tapping an agent's timeline bar shows that round's model turns and tool calls with their durations, ordered chronologically or slowest-first, plus totals per tool/model.
- Tapping the coordinator's bar shows the same for the coordinator (bounded, with totals covering everything).
- Tapping a step shows its transcript row (tool arguments/output or model text) when one exists.
- Shell-job bars still open the job's output.
- Go tests pass; the iOS app builds and its tests pass on the Mac mini.

## Work log
