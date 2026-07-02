---
id: "0118"
title: 'ListWorkstreams enrichment: reuse the project repo instead of re-opening per workstream'
status: done
priority: 5
created: "2026-07-02"
updated: "2026-07-02"
depends_on:
    - "0085"
spec_refs: []
---

## Description
## Context
Review note from task 0085: `Manager.WorkstreamCommitCount` calls `primaryRepo` → `git.Open` (which spawns `git rev-parse`) once per non-terminal workstream on every `ListWorkstreams` call, and the TUI Workstreams panel polls this every ~3s. Redundant subprocess work at scale.

## Scope
Open/resolve each project's primary repo once per ListWorkstreams call (or cache per project on the Manager), reusing it for all of that project's workstream enrichment (commit count, and any future fields).

## Acceptance criteria
- [ ] ListWorkstreams enrichment opens each project's repo at most once per call.
- [ ] Behaviour unchanged (best-effort, 0/empty on error); build/vet/test pass.

## Acceptance criteria

## Plan

Goal: stop `ListWorkstreams` enrichment from re-opening (git.Open → `git rev-parse` subprocess) the same project's primary repo once per workstream on every call (TUI polls ~3s).

Approach (per-call cache, no long-lived Manager state so project re-resolution stays fresh):
1. In internal/session, add a batch enrichment path so a single ListWorkstreams call opens each project's primary repo at most once. Preferred shape: a Manager method like `WorkstreamCommitCounts(wss []workstream.Workstream) map[string]int` (keyed by workstream ID) that internally caches `projects.Resolve` + `git.Open` results per project name (cache errors too, so a broken project isn't retried per workstream). Reuse the existing count logic (skip when Branch/BaseCommit empty; best-effort 0 on any error). Keep `WorkstreamCommitCount` for any other callers, or refactor it to share the same core — implementer's choice, but don't change its behaviour.
2. Update `Server.ListWorkstreams` (internal/server/workstream.go) to compute counts once for the non-terminal workstreams via the new batch method, then use them in the enrichment loop. `SessionStatus` enrichment is in-memory and unchanged.
3. Add/adjust a test: e.g. in internal/server/workstream_rpc_test.go or internal/session, assert the batch method returns the same counts as the per-workstream method for a multi-workstream project, and (if feasible cheaply) that behaviour is unchanged for unknown projects / missing base commit (returns 0, no error).
4. Verify: go build ./... && go vet ./... && go test ./...

Acceptance: enrichment opens each project's repo at most once per ListWorkstreams call; behaviour unchanged (best-effort 0/empty on error); build/vet/test pass.

### Starting points
- internal/session/workstream_merge.go:93-137 — primaryRepo (projects.Resolve + git.Open) and WorkstreamCommitCount
- internal/server/workstream.go:80-91 — ListWorkstreams enrichment loop
- internal/server/workstream_rpc_test.go — existing RPC tests for ListWorkstreams
- git.Open spawns `git rev-parse` per call — that's the cost being deduplicated

## Work log
- 2026-07-02 plan: Goal: stop `ListWorkstreams` enrichment from re-opening (git.Open → `git rev-parse` subprocess) the same project's primary repo once per workstream on every call (TUI polls ~3s).  Approach (per-call
…[truncated]
- 2026-07-02 context hints: 4 recorded with plan
- 2026-07-02 context hints: internal/session/workstream_merge.go:93-137 — primaryRepo (projects.Resolve + git.Open) and WorkstreamCommitCount; internal/server/workstream.go:80-91 — ListWorkstreams enrichment loop; internal/s
…[truncated]
- 2026-07-02 implementer report: Deduplicated per-workstream repo opening in ListWorkstreams enrichment.  ## Changes - internal/session/workstream_merge.go   - Extracted a shared `commitCount(repo *git.Repo, ws)` helper (best-effort:
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The change adds a batch Manager.WorkstreamCommitCounts that resolves/opens each project's primary repo at most once per call (caching a nil repo for failed resolves so broken projects aren't retried),
…[truncated]
- 2026-07-02 decision: accept — commit: session: batch commit-count enrichment so ListWorkstreams opens each project repo once (task 0118)
