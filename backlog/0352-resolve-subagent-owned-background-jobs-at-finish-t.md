---
id: "0352"
title: Resolve subagent-owned background jobs at finish through cleanup or explicit handoff
status: done
priority: 2
created: "2026-09-08"
updated: "2026-09-09"
depends_on: []
spec_refs:
    - §7.3 Subagents and asynchronous jobs
---

## Description
Resolve subagent-owned background work at completion so reports are not lost and live mutating processes cannot silently outlast their owner.

## Acceptance criteria
- Completed owned jobs are accounted for in the subagent result or an explicit parent handoff.
- Running jobs are terminated and joined or explicitly transferred with IDs, purpose, ownership, and delivery semantics; watcher continuation is opt-in.
- Mutation ownership lasts until execution actually stops, including cancellation and session shutdown.
- Preserve exactly-once notifications and repeatable evidence retrieval.
- Cover finish, failure/cancellation, handoff delivery, retained follow-up, and subsequent mutation gating.

## Outcome
Implemented default child cancellation/join and report accounting for fresh and retained implementers, plus explicit `finish(handoff_jobs)` watcher transfer with durable replayable ownership and delivery metadata. Tracked execution lifetime is separate from terminal report state; shutdown atomically closes registration and joins accepted runners. Child evidence survives no-progress and error results. Updated the async-jobs design contract.

Both independent reviewers accepted after revision. The isolated task-only tree passes `go test ./...`, race tests for jobs/tools/engine/orchestrator, and targeted session-stop race tests. Unrelated pre-existing changes are excluded from the commit.

Commit subject: Resolve subagent background jobs at completion and shutdown
