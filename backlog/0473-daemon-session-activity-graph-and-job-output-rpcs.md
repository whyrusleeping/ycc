---
id: "0473"
title: 'Daemon: session activity graph and job output RPCs'
status: done
priority: 2
created: "2026-10-21"
updated: "2026-10-21"
depends_on: []
spec_refs:
    - spec.md#7.3
    - docs/remote-api.md
---

## Description
## Why
Users want a graphical view of a session's agents, subagents and background jobs (web first, then iOS). They want to drill into what each agent is doing and read shell job output. Today clients can't read job output, and nothing projects the agent/job structure.

## Approach
- The sessionview index keeps a compact copy of the activity-relevant events (subagent_spawned/finished, job_started/finished/claimed/notified/handed_off), with large strings truncated. That keeps polling cheap on 10 MB logs.
- `GetSessionActivity(project, session_id)` builds a node list from those events:
  - the coordinator root;
  - one actor node per subagent (`agent:<id>`, `implementer`, `reviewer:<model>`), with one span per spawn→finish round, linked to its job id when it has one;
  - one node per non-agent background job (bash), parented to the actor that started it.
- For a live session, running spans and jobs are overlaid with registry state: status, current tool, turns and tokens. For a session that is no longer live, unfinished work shows as lost.
- `GetJobOutput(project, session_id, job_id, offset, limit, tail_lines)` reads a live session's retained job buffer through absolute byte cursors. When the session isn't live, or the output wasn't retained, it says so explicitly, and the client falls back to the node's final result.

## Acceptance criteria
- For any session, live or persisted, a client can fetch its coordinator → subagent → background-job tree with statuses, models, timings and round spans.
- Running agent nodes of a live session expose current tool, turns and token totals.
- A client can tail or page a live background shell job's output by absolute cursor. Unavailable output is reported, not an error.
- Both RPCs use the same project/session resolution and auth as GetSessionView. Go, Swift and web generated code are regenerated, and docs/remote-api.md documents the RPCs.

## Work log
