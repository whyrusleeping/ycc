---
id: "0401"
title: 'Daemon: faster ListProjects (parallel single-pass git status) + SubscribeSessionView heartbeat'
status: done
priority: 1
created: "2026-09-28"
updated: "2026-09-28"
depends_on: []
spec_refs: []
---

## Description
Evidence (live /debug/latency, 2026-09-28): ListProjects p50 ~100ms because ProjectGitStatus runs 3 git subprocesses serially per project (rev-parse, status --porcelain, rev-list) across 6 projects; iOS calls it on every return home and every 10s while the drawer is open. SubscribeSessionView sends nothing on a quiet session, so iOS URLSession's default 60s inter-data timeout (timeoutIntervalForRequest) kills the stream → reconnecting flicker + full GetSessionView re-download every minute.

Acceptance:
- ProjectGitStatus uses one `git status --porcelain=v2 --branch` invocation (branch, upstream, ahead/behind, dirty) with identical semantics (detached HEAD → empty branch, no upstream → HasUpstream=false).
- ListProjects computes per-project git status/onboarding concurrently (bounded), preserving order.
- SubscribeSessionView sends an empty keepalive SessionViewUpdate after ~20s without traffic; existing iOS client ignores it (no state, no transient event → no-op branch). Test covers heartbeat emission with an injectable interval.
- go test for touched packages green.

## Acceptance criteria

## Work log
