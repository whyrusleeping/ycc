---
id: "0351"
title: Make jobs discoverable with repeatable results and cursor-based output retrieval
status: done
priority: 2
created: "2026-09-08"
updated: "2026-09-09"
depends_on: []
spec_refs:
    - §7.3 Subagents and asynchronous jobs
    - §8 Tools and access policy
---

## Description

Exactly-once final notifications currently overlap with result consumption: there is no list_jobs tool, job_output advances an implicit cursor, and agent jobs expose no useful incremental activity. Separate notification delivery from access to retained evidence. Evidence: internal/tools/jobs.go:21–51; internal/jobs/jobs.go:99–133,187–208.

## Acceptance criteria

- Models can enumerate relevant live/completed jobs with stable IDs, owners, state, and elapsed time.
- A final result remains explicitly retrievable after its exactly-once notification was delivered; retrieval does not create duplicate automatic notifications.
- Output reads support explicit cursors/ranges or tail requests, identify retention gaps, and can revisit retained data; integrate artifacts from 0323 without blocking basic retrieval on a full artifact redesign.
- Agent status exposes bounded last-activity/current-tool/turn/usage summaries without revealing private reasoning.
- Tests cover repeated retrieval, wait-versus-checkpoint races, evicted output, multiple actors, unknown IDs, and cancelled/lost-on-restart jobs.
- Descriptions accurately distinguish progress, notification consumption, and repeatable evidence retrieval.

## Outcome

Added actor-scoped job discovery, repeatable final results and explicit cursor/tail output with retention gaps, safe agent activity summaries, and durable restart restoration with stable IDs. Fixed empty-wait ownership and notification/finish crash-window races. Both reviewers accepted; go test ./... and focused jobs/tools/engine/orchestrator race tests passed. Unrelated pre-existing changes were isolated in a stash and are restored after this task-only commit.

Commit: Make background jobs discoverable with repeatable retained evidence
